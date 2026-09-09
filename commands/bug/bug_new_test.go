package bugcmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/git-bug/git-bug/commands/bug/testenv"
)

func TestBugNew(t *testing.T) {
	env, _ := testenv.NewTestEnvAndUser(t)

	err := runBugNew(env, bugNewOptions{
		nonInteractive: true,
		message:        "message",
		title:          "title",
	})
	require.NoError(t, err)
	require.Regexp(t, "^[0-9A-Fa-f]{7} created\n$", env.Out.String())
}

func TestBugNewTemplate(t *testing.T) {
	// Use `cat` as the editor: it simply prints the pre-filled file back,
	// acting as a no-op edit so we can inspect what the user would have seen.
	t.Setenv("GIT_EDITOR", "cat")

	// A shell command whose stdout becomes the pre-filled title and message.
	t.Setenv("GIT_BUG_NEW_TEMPLATE", "printf 'templated title\\ntemplated message\\n'")

	env, _ := testenv.NewTestEnvAndUser(t)

	// Non-interactive is left false and no title/message are provided, so the
	// editor input path (and thus the template) is exercised.
	err := runBugNew(env, bugNewOptions{})
	require.NoError(t, err)
	require.Regexp(t, "^[0-9A-Fa-f]{7} created\n$", env.Out.String())
}
