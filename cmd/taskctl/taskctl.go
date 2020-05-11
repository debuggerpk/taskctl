package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/logrusorgru/aurora"
	"github.com/manifoldco/promptui"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/taskctl/taskctl/internal/config"
	"github.com/taskctl/taskctl/internal/output"
	"github.com/taskctl/taskctl/internal/utils"
)

var version = "dev"

var cancel = make(chan struct{})
var done = make(chan bool)

var cfg *config.Config

func main() {
	logrus.SetFormatter(&logrus.TextFormatter{
		DisableColors:   false,
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   false,
	})
	listenSignals()

	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func listenSignals() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigs {
			abort()
			os.Exit(int(sig.(syscall.Signal)))
		}
	}()
}

func run() error {
	cl := config.NewConfigLoader()

	app := &cli.App{
		Name:                 "taskctl",
		Usage:                "modern task runner",
		Version:              version,
		EnableBashCompletion: true,
		BashComplete: func(c *cli.Context) {
			cfg, _ = cl.Load(c.String("config"))
			suggestions := buildSuggestions(cfg)

			for _, v := range suggestions {
				candidate := strings.ReplaceAll(v.Target, ":", "\\:")
				fmt.Printf("%s\n", candidate)
			}

			cli.DefaultAppComplete(c)
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug",
				EnvVars: []string{"TASKCTL_DEBUG"},
			},
			&cli.PathFlag{
				Name:        "config",
				Aliases:     []string{"c"},
				Usage:       "config file to use",
				EnvVars:     []string{"TASKCTL_CONFIG_FILE"},
				DefaultText: "tasks.yaml or taskctl.yaml",
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "output format (raw, prefixed or cockpit)",
				EnvVars: []string{"TASKCTL_OUTPUT_FORMAT"},
			},
			&cli.BoolFlag{
				Name:    "raw",
				Aliases: []string{"r"},
				Usage:   "shortcut for --output=raw",
			},
			&cli.BoolFlag{
				Name:  "cockpit",
				Usage: "shortcut for --output=cockpit",
			},
			&cli.BoolFlag{
				Name:    "quiet",
				Aliases: []string{"q"},
				Usage:   "quite mode",
			},
			&cli.StringSliceFlag{
				Name:  "set",
				Usage: "set global variable value",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "dry run",
			},
			&cli.BoolFlag{
				Name:    "summary",
				Usage:   "show summary",
				Aliases: []string{"s"},
				Value:   true,
			},
		},
		Before: func(c *cli.Context) (err error) {
			cfg, err = cl.Load(c.String("config"))
			if err != nil && (c.IsSet("config") && errors.Is(err, config.ErrConfigNotFound) || !errors.Is(err, config.ErrConfigNotFound)) {
				return err
			}

			for _, c := range c.StringSlice("set") {
				arr := strings.Split(c, "=")
				if len(arr) > 1 {
					cfg.Variables.Set(arr[0], strings.Join(arr[1:], "="))
				}
			}

			if c.Bool("debug") || cfg.Debug {
				logrus.SetLevel(logrus.DebugLevel)
			} else {
				logrus.SetLevel(logrus.InfoLevel)
			}

			if c.Bool("quiet") {
				logrus.SetOutput(ioutil.Discard)
				output.SetStdout(ioutil.Discard)
				output.SetStderr(ioutil.Discard)
			}

			if c.IsSet("output") {
				cfg.Output = c.String("output")
			} else if c.Bool("raw") {
				cfg.Output = output.OutputFormatRaw
			} else if c.Bool("cockpit") {
				cfg.Output = output.OutputFormatCockpit
			}

			return nil
		},
		Action: func(c *cli.Context) (err error) {
			runCommand := c.App.Command("run")
			err = runCommand.Before(c)
			if err != nil {
				return err
			}

			taskRunner, err := buildTaskRunner(c)
			if err != nil {
				return err
			}

			targets := c.Args().Slice()
			if len(targets) > 0 {
				for _, target := range targets {
					if target == "--" {
						break
					}

					err = runTarget(target, c, taskRunner)
					if err != nil {
						return err
					}
				}
				return nil
			}

			suggestions := buildSuggestions(cfg)
			targetSelect := promptui.Select{
				Label:        "Select task to run",
				Items:        suggestions,
				Size:         15,
				CursorPos:    0,
				IsVimMode:    false,
				HideHelp:     false,
				HideSelected: false,
				Templates: &promptui.SelectTemplates{
					Active:   fmt.Sprintf("%s {{ .DisplayName | underline }}", promptui.IconSelect),
					Inactive: "  {{ .DisplayName }}",
					Selected: fmt.Sprintf(`{{ "%s" | green }} {{ .DisplayName | faint }}`, promptui.IconGood),
				},
				Keys: nil,
				Searcher: func(input string, index int) bool {
					return strings.Contains(suggestions[index].DisplayName, input)
				},
				StartInSearchMode: true,
			}

			fmt.Println("Please use `Ctrl-C` to exit this program.")
			index, _, err := targetSelect.Run()
			if err != nil {
				return err
			}

			selection := suggestions[index]
			if selection.IsTask {
				return runTask(cfg.Tasks[selection.Target], taskRunner)
			}

			return runPipeline(cfg.Pipelines[selection.Target], taskRunner, c.Bool("summary"))
		},
		Commands: []*cli.Command{
			newRunCommand(),
			newInitCommand(),
			newListCommand(),
			newShowCommand(),
			newWatchCommand(),
			newCompletionCommand(),
			newGraphCommand(),
		},
		Authors: []*cli.Author{
			{
				Name:  "Yevhen Terentiev",
				Email: "yevhen.terentiev@gmail.com",
			},
		},
	}

	return app.Run(os.Args)
}

func abort() {
	close(cancel)
	<-done
}

type suggestion struct {
	Target, DisplayName string
	IsTask              bool
}

func buildSuggestions(cfg *config.Config) []suggestion {
	suggestions := make([]suggestion, 0)

	for _, v := range utils.MapKeys(cfg.Pipelines) {
		suggestions = append(suggestions, suggestion{
			Target:      v,
			DisplayName: fmt.Sprintf("%s - %s", v, aurora.Gray(12, "pipeline").String()),
		})
	}

	for k, v := range cfg.Tasks {
		desc := "task"
		if v.Description != "" {
			desc = v.Description
		}
		suggestions = append(suggestions, suggestion{
			Target:      k,
			DisplayName: fmt.Sprintf("%s - %s", k, aurora.Gray(12, desc).String()),
			IsTask:      true,
		})
	}

	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[j].Target > suggestions[i].Target
	})

	return suggestions
}
