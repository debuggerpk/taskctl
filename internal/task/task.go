package task

import (
	"bytes"
	"time"

	"github.com/taskctl/taskctl/internal/utils"
)

type Task struct {
	Index        uint32
	Command      []string
	Context      string
	Env          *utils.Variables
	Variables    *utils.Variables
	Variations   []map[string]string
	Dir          string
	Timeout      *time.Duration
	AllowFailure bool
	After        []string

	Condition string
	Skipped   bool

	Name        string
	Description string

	Start time.Time
	End   time.Time

	ExportAs string

	ExitCode int
	Errored  bool
	Error    error
	Log      struct {
		Stderr bytes.Buffer
		Stdout bytes.Buffer
	}
}

func NewTask() *Task {
	return &Task{
		Env:       utils.NewVariables(nil),
		Variables: utils.NewVariables(nil),
	}
}

func (t *Task) Duration() time.Duration {
	if t.End.IsZero() {
		return time.Since(t.Start)
	}

	return t.End.Sub(t.Start)
}

func (t *Task) ErrorMessage() string {
	if t.Log.Stderr.Len() > 0 {
		return utils.LastLine(&t.Log.Stderr)
	}

	return utils.LastLine(&t.Log.Stdout)
}

func (t *Task) Interpolate(s string, params ...*utils.Variables) (string, error) {
	data := t.Variables

	for _, variables := range params {
		data = data.Merge(variables)
	}

	return utils.RenderString(s, data.Map())
}
