package buginput

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/pkg/errors"

	"github.com/git-bug/git-bug/commands/input"
	"github.com/git-bug/git-bug/repository"
)

const messageFilenameBase = "BUG_MESSAGE_EDITMSG"

// newMessageFilename returns a unique message filename for a single editor
// invocation. A unique name (PID + sequence) avoids collisions with stale
// editor swap files or other concurrent git-bug invocations that would
// otherwise reuse the same path. Underscores (not dots) are used so editors
// don't mis-detect the numeric suffix as a file type.
func newMessageFilename() string {
	return fmt.Sprintf("%s_%d_%d", messageFilenameBase, os.Getpid(), messageFilenameSeq.Add(1))
}

var messageFilenameSeq atomic.Uint64

// Template commands pre-populate editor prompts with shell-generated text.
// Each editor prompt has its own config key and environment variable, and the
// environment variable takes precedence over the config entry.
const (
	// For the 'git-bug bug new' prompt.
	newTemplateConfigKey = "git-bug.new.template"
	newTemplateEnvKey    = "GIT_BUG_NEW_TEMPLATE"

	// For the 'git-bug bug comment new' and 'bug comment edit' prompts.
	commentTemplateConfigKey = "git-bug.comment.template"
	commentTemplateEnvKey    = "GIT_BUG_COMMENT_TEMPLATE"
)

// AnyConfigReader is the minimal surface of a repository needed to read
// the merged local/global config. Both *cache.RepoCache and *repository.GoGitRepo
// satisfy it.
type AnyConfigReader interface {
	AnyConfig() repository.ConfigRead
}

// RunTemplateCommand runs the template command configured for an editor
// prompt, if any. The command is looked up from the environment variable
// named by envKey, falling back to the git config entry with key configKey.
// It is run in the current working directory (so it can inspect the state of
// the repository, e.g. with 'git status'), and its stdout is returned for the
// caller to pre-fill the editor template.
//
// The configured value is the path to an executable script (not a shell
// command line): leading '~' and $VAR/${VAR} references are expanded, and
// extra args are passed to the script as positional parameters ($1, $2, ...),
// so a single script can behave differently per prompt, e.g.
// 'case "$1" in new|edit) ...' for the comment prompt.
//
// It returns ("", nil) when no command is configured. A non-zero exit status
// is an error, with the command's stderr included in the message.
func RunTemplateCommand(repo interface{}, configKey, envKey string, args ...string) (raw string, err error) {
	cmd, err := lookupTemplateCommand(repo, configKey, envKey)
	if err != nil {
		return "", err
	}
	cmd = expandTemplateCommand(cmd)
	if strings.TrimSpace(cmd) == "" {
		return "", nil
	}

	words := strings.Fields(cmd)

	var stdout, stderr bytes.Buffer
	c := exec.Command(words[0], append(append([]string{}, words[1:]...), args...)...)
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err = c.Run(); err != nil {
		err = fmt.Errorf("%s\nbug template command failed: %w\n\n",
			strings.TrimSpace(stderr.String()),
			err,
		)
	}

	return stdout.String(), err
}

// expandTemplateCommand expands a leading '~' to $HOME and $VAR/${VAR}
// references in a template command path.
func expandTemplateCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "~" || strings.HasPrefix(cmd, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cmd = filepath.Join(home, strings.TrimPrefix(cmd, "~/"))
		}
	}
	return os.ExpandEnv(cmd)
}

// lookupTemplateCommand returns the shell command configured to pre-populate
// an editor prompt, or "" if none is set. The environment variable named by
// envKey takes precedence over the git config entry keyed by configKey.
func lookupTemplateCommand(repo interface{}, configKey, envKey string) (string, error) {
	if cmd, ok := os.LookupEnv(envKey); ok && strings.TrimSpace(cmd) != "" {
		return cmd, nil
	}

	cfg, ok := repo.(AnyConfigReader)
	if !ok {
		return "", nil
	}
	cmd, err := repository.GetDefaultString(configKey, cfg.AnyConfig(), "")
	if err != nil {
		return "", err
	}
	return cmd, nil
}

// ErrEmptyMessage is returned when the required message has not been entered
var ErrEmptyMessage = errors.New("empty message")

// ErrEmptyTitle is returned when the required title has not been entered
var ErrEmptyTitle = errors.New("empty title")

const bugTitleCommentTemplate = `%s%s

# Please enter the title and comment message. The first non-empty line will be
# used as the title. Lines starting with '#' will be ignored.
# An empty title aborts the operation.
`

// BugCreateEditorInput will open the default editor in the terminal with a
// template for the user to fill. The file is then processed to extract title
// and message.
//
// If the git-bug.new.template config is set, the command it names is
// run (in the current directory) and its output is used to pre-fill the editor.
func BugCreateEditorInput(repo repository.RepoCommonStorage, preTitle string, preMessage string) (string, string, error) {
	txt, err := RunTemplateCommand(repo, newTemplateConfigKey, newTemplateEnvKey)
	if err != nil {
		txt = err.Error() + "\n\n" + txt
	}
	if preMessage != "" {
		preMessage = "\n\n" + preMessage
	}
	preMessage += txt

	template := fmt.Sprintf(bugTitleCommentTemplate, preTitle, preMessage)

	raw, err := input.LaunchEditorWithTemplate(repo, newMessageFilename(), template)
	if err != nil {
		return "", "", err
	}

	return processCreate(raw)
}

// BugCreateFileInput read from either from a file or from the standard input
// and extract a title and a message
func BugCreateFileInput(fileName string) (string, string, error) {
	raw, err := input.FromFile(fileName)
	if err != nil {
		return "", "", err
	}

	return processCreate(raw)
}

func processCreate(raw string) (string, string, error) {
	lines := strings.Split(raw, "\n")

	var title string
	var buffer bytes.Buffer
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}

		if title == "" {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				title = trimmed
			}
			continue
		}

		buffer.WriteString(line)
		buffer.WriteString("\n")
	}

	if title == "" {
		return "", "", ErrEmptyTitle
	}

	message := strings.TrimSpace(buffer.String())

	return title, message, nil
}

const bugCommentTemplate = `%s

# Please enter the comment message. Lines starting with '#' will be ignored,
# and an empty message aborts the operation.
`

// BugCommentEditorInput will open the default editor in the terminal with a
// template for the user to fill. The file is then processed to extract a comment.
//
// If the git-bug.comment.template config (or GIT_BUG_COMMENT_TEMPLATE) is set,
// the command it names is run (in the current directory) and its output is
// used to pre-fill the editor. The script is invoked with five arguments:
// $1 is the mode, "new" for 'bug comment new' or "edit" for 'bug comment
// edit', $2 is the short ID of the comment being edited (empty for a new
// comment, which does not exist until the editor is saved), $3 is the short
// ID of the bug, $4 is the bug's title, and $5 is the comment's compiled
// text (empty for a new comment). Passing the title and the comment text as
// arguments (rather than having the script run 'git-bug') avoids the script
// re-locking the repository that is already locked by this command.
func BugCommentEditorInput(repo repository.RepoCommonStorage, preMessage string, mode string, commentId string, bugId string, title string, commentText string) (string, error) {
	txt, err := RunTemplateCommand(repo, commentTemplateConfigKey, commentTemplateEnvKey, mode, commentId, bugId, title, commentText)
	if err != nil {
		txt = err.Error() + "\n\n" + txt
	}
	preMessage += txt

	if preMessage != "" {
		preMessage = "\n\n" + preMessage
	}

	template := fmt.Sprintf(bugCommentTemplate, preMessage)

	raw, err := input.LaunchEditorWithTemplate(repo, newMessageFilename(), template)
	if err != nil {
		return "", err
	}

	return processComment(raw)
}

// BugCommentFileInput read from either from a file or from the standard input
// and extract a message
func BugCommentFileInput(fileName string) (string, error) {
	raw, err := input.FromFile(fileName)
	if err != nil {
		return "", err
	}

	return processComment(raw)
}

func processComment(raw string) (string, error) {
	lines := strings.Split(raw, "\n")

	var buffer bytes.Buffer
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		buffer.WriteString(line)
		buffer.WriteString("\n")
	}

	message := strings.TrimSpace(buffer.String())

	if message == "" {
		return "", ErrEmptyMessage
	}

	return message, nil
}

const bugTitleTemplate = `%s

# Please enter the new title. Only one line will used.
# Lines starting with '#' will be ignored, and an empty title aborts the operation.
`

// BugTitleEditorInput will open the default editor in the terminal with a
// template for the user to fill. The file is then processed to extract a title.
func BugTitleEditorInput(repo repository.RepoCommonStorage, preTitle string) (string, error) {
	template := fmt.Sprintf(bugTitleTemplate, preTitle)

	raw, err := input.LaunchEditorWithTemplate(repo, newMessageFilename(), template)
	if err != nil {
		return "", err
	}

	lines := strings.Split(raw, "\n")

	var title string
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		title = trimmed
		break
	}

	if title == "" {
		return "", ErrEmptyTitle
	}

	return title, nil
}

const queryTemplate = `%s

# Please edit the bug query.
# Lines starting with '#' will be ignored, and an empty query aborts the operation.
#
# Example: status:open author:"rené descartes" sort:edit
#
# Valid filters are:
#
# - status:open, status:closed
# - author:<query>
# - title:<title>
# - label:<label>
# - no:label
#
# Sorting
#
# - sort:id, sort:id-desc, sort:id-asc
# - sort:creation, sort:creation-desc, sort:creation-asc
# - sort:edit, sort:edit-desc, sort:edit-asc
#
# Notes
#
# - queries are case insensitive.
# - you can combine as many qualifiers as you want.
# - you can use double quotes for multi-word search terms (ex: author:"René Descartes")
`

// QueryEditorInput will open the default editor in the terminal with a
// template for the user to fill. The file is then processed to extract a query.
func QueryEditorInput(repo repository.RepoCommonStorage, preQuery string) (string, error) {
	template := fmt.Sprintf(queryTemplate, preQuery)

	raw, err := input.LaunchEditorWithTemplate(repo, newMessageFilename(), template)
	if err != nil {
		return "", err
	}

	lines := strings.Split(raw, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return trimmed, nil
	}

	return "", nil
}
