package cmd

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Diniboy1123/usque/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run configured modes concurrently",
	Long:  "Starts the operational modes listed in the serve array in config.json, concurrently in this process.",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if !config.ConfigLoaded {
			cmd.Println("Config not loaded. Please register first.")
			return
		}
		if len(config.AppConfig.Serve) == 0 {
			cmd.Println("No serve entries configured. Add a serve array to config.json.")
			return
		}

		modeCommands := map[string]*cobra.Command{
			"http-proxy":     httpProxyCmd,
			"l4-http-proxy":  l4HTTPProxyCmd,
			"l4-socks":       l4SocksCmd,
			"nativetun":      nativeTunCmd,
			"portfw":         portFwCmd,
			"socks":          socksCmd,
		}

		type task struct {
			mode string
			cmd  *cobra.Command
		}
		tasks := make([]task, 0, len(config.AppConfig.Serve))
		for i, entry := range config.AppConfig.Serve {
			mode, err := entry.NormalizedMode()
			if err != nil {
				cmd.Printf("Invalid serve entry %d: %v\n", i, err)
				return
			}
			modeCmd := modeCommands[mode]
			if modeCmd == nil || modeCmd.Run == nil {
				cmd.Printf("Serve entry %d: mode %q is unavailable\n", i, mode)
				return
			}

			instance, err := newModeCommand(modeCmd, entry.Options)
			if err != nil {
				cmd.Printf("Invalid serve entry %d (%s): %v\n", i, mode, err)
				return
			}
			tasks = append(tasks, task{mode: mode, cmd: instance})
		}

		var wg sync.WaitGroup
		for _, current := range tasks {
			current := current
			wg.Add(1)
			go func() {
				defer wg.Done()
				log.Printf("Starting configured %s mode", current.mode)
				modeCommands[current.mode].Run(current.cmd, nil)
			}()
		}
		wg.Wait()
	},
}

// newModeCommand creates an isolated command and flag set for one configured mode. The
// existing mode handlers can then be reused safely for multiple concurrent serve entries.
func newModeCommand(source *cobra.Command, options map[string]any) (*cobra.Command, error) {
	flags, err := cloneFlagSet(source.Flags())
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	// Sort for deterministic validation errors and logs.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, key := range keys {
		name := strings.ReplaceAll(strings.ToLower(key), "_", "-")
		flag := flags.Lookup(name)
		if flag == nil {
			return nil, fmt.Errorf("unknown option %q", key)
		}
		if err := setServeFlag(flag, options[key]); err != nil {
			return nil, fmt.Errorf("option %q: %w", key, err)
		}
	}

	instance := &cobra.Command{Use: source.Use, SilenceUsage: true}
	instance.SetOut(source.OutOrStdout())
	instance.SetErr(source.ErrOrStderr())
	instance.SetIn(source.InOrStdin())
	instance.Flags().AddFlagSet(flags)
	return instance, nil
}

func cloneFlagSet(source *pflag.FlagSet) (*pflag.FlagSet, error) {
	cloned := pflag.NewFlagSet(source.Name(), pflag.ContinueOnError)
	var cloneErr error
	source.VisitAll(func(flag *pflag.Flag) {
		if cloneErr != nil {
			return
		}
		usage, name, shorthand, def := flag.Usage, flag.Name, flag.Shorthand, flag.DefValue
		switch flag.Value.Type() {
		case "string":
			cloned.StringP(name, shorthand, def, usage)
		case "bool":
			value, err := strconv.ParseBool(def)
			if err != nil {
				cloneErr = err
				return
			}
			cloned.BoolP(name, shorthand, value, usage)
		case "int":
			value, err := strconv.Atoi(def)
			if err != nil {
				cloneErr = err
				return
			}
			cloned.IntP(name, shorthand, value, usage)
		case "uint16":
			value, err := strconv.ParseUint(def, 10, 16)
			if err != nil {
				cloneErr = err
				return
			}
			cloned.Uint16P(name, shorthand, uint16(value), usage)
		case "duration":
			value, err := time.ParseDuration(def)
			if err != nil {
				cloneErr = err
				return
			}
			cloned.DurationP(name, shorthand, value, usage)
		case "stringArray":
			cloned.StringArrayP(name, shorthand, stringArrayDefault(def), usage)
		default:
			cloneErr = fmt.Errorf("unsupported flag type %q for --%s", flag.Value.Type(), name)
		}
		if cloned.Lookup(name) != nil {
			cloned.Lookup(name).NoOptDefVal = flag.NoOptDefVal
		}
	})
	if cloneErr != nil {
		return nil, cloneErr
	}
	return cloned, nil
}

func stringArrayDefault(def string) []string {
	trimmed := strings.TrimSpace(def)
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	if trimmed == "" {
		return nil
	}
	return strings.Fields(trimmed)
}

func setServeFlag(flag *pflag.Flag, value any) error {
	if value == nil {
		return nil
	}
	if flag.Value.Type() == "stringArray" {
		var values []string
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				values = append(values, fmt.Sprint(item))
			}
		case []string:
			values = append(values, v...)
		case string:
			values = append(values, v)
		default:
			return fmt.Errorf("expected a string or array of strings")
		}
		var cloned []string
		flag.Value = pflag.NewStringArrayValue(values, &cloned)
		flag.DefValue = fmt.Sprint(values)
		flag.Changed = true
		return nil
	}
	if flag.Value.Type() == "bool" {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected a boolean")
		}
	}
	if err := flag.Value.Set(fmt.Sprint(value)); err != nil {
		return err
	}
	flag.Changed = true
	return nil
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
