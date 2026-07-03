package permission

import (
	"strings"

	"reasonix/internal/shellsafe"
)

// DecomposeBashCommand splits a compound bash command line into its
// simple-command segments so each segment can be matched against the rule
// table independently. This is the mechanism Claude Code and comparable
// harnesses use to make prefix rules like `Bash(git push:*)` reusable across
// compound invocations without ever synthesizing a new prefix from a compound
// command.
//
// It splits on the shell control operators `;`, `&`, `&&`, `|`, `||`, and
// newlines. Quoting (single, double, backslash-escapes inside double quotes)
// and $(...) / <(...) / >(...) / `...` command / process substitutions are
// treated as opaque — operators inside them do NOT split the outer command.
// File-descriptor duplication like `2>&1` is recognized (the `&` following an
// unquoted `>` does not split).
//
// Known out-of-scope shapes — the parser refuses to decompose these to keep
// downstream matching safe, so callers fall back to whole-string matching:
//   - heredocs (`cat <<EOF … EOF`): the delimiter body isn't shell syntax,
//     but tokenizing it as one is wrong; we bail on any unquoted `<<`.
//   - leading operator (`&& ls`, `; ls`): malformed shell.
//   - unbalanced quotes, `$(...)`, `<(...)`, `>(...)`, or backticks.
//
// Returns nil when the input has no control operator to split on, or when the
// parser encounters one of the above out-of-scope shapes. Redirect fragments
// (`2>/dev/null`, `> file`) are left attached to the simple command they
// annotate; stripping those is out of scope for this pass.
//
// The tokenizer is hand-rolled to avoid pulling in a full shell parser (e.g.
// `mvdan.cc/sh`) for what is otherwise a small piece of permission-layer
// logic. If a maintainer prefers the dep, swapping is straightforward — the
// only contract this function exposes is `[]string` of trimmed simple-command
// text, or `nil` for "fall back to exact match".
func DecomposeBashCommand(cmd string) []string {
	if !shellsafe.ContainsShellSyntax(cmd) {
		return nil
	}
	// Malformed shell: leading control operator with nothing before it.
	trimmed := strings.TrimLeft(cmd, " \t\n\r")
	for _, op := range []string{"&&", "||", ";", "|", "&"} {
		if strings.HasPrefix(trimmed, op) {
			return nil
		}
	}

	var (
		out        []string
		buf        strings.Builder
		quote      byte // 0, '\'', or '"'
		parenDepth int  // depth of $(...) / <(...) / >(...)
		backtick   bool
		split      bool // did we actually hit any splitter?
	)

	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}

	for i := 0; i < len(cmd); i++ {
		c := cmd[i]

		switch {
		case quote != 0:
			buf.WriteByte(c)
			if c == '\\' && quote == '"' && i+1 < len(cmd) {
				buf.WriteByte(cmd[i+1])
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		case parenDepth > 0:
			buf.WriteByte(c)
			switch c {
			case '(':
				parenDepth++
			case ')':
				parenDepth--
			}
			continue
		case backtick:
			buf.WriteByte(c)
			if c == '`' {
				backtick = false
			}
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
			buf.WriteByte(c)
		case '`':
			backtick = true
			buf.WriteByte(c)
		case '\\':
			buf.WriteByte(c)
			if i+1 < len(cmd) {
				buf.WriteByte(cmd[i+1])
				i++
			}
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				parenDepth = 1
				buf.WriteByte(c)
				buf.WriteByte('(')
				i++
				continue
			}
			buf.WriteByte(c)
		case '<':
			// Heredoc (`<<EOF ... EOF`) can't be safely decomposed —
			// its body isn't shell syntax but tokenizing it as one is wrong.
			if i+1 < len(cmd) && cmd[i+1] == '<' {
				return nil
			}
			// Process substitution `<(cmd)`: track as opaque like `$(...)`.
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				parenDepth = 1
				buf.WriteByte(c)
				buf.WriteByte('(')
				i++
				continue
			}
			buf.WriteByte(c)
		case '>':
			// Process substitution `>(cmd)`: track as opaque like `$(...)`.
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				parenDepth = 1
				buf.WriteByte(c)
				buf.WriteByte('(')
				i++
				continue
			}
			buf.WriteByte(c)
		case ';', '\n', '\r':
			flush()
			split = true
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				i++
			}
			flush()
			split = true
		case '&':
			// `>&` (fd duplication) is not a splitter. Look at the last
			// non-whitespace byte written to buf.
			if lastMeaningfulByte(&buf) == '>' {
				buf.WriteByte(c)
				continue
			}
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
			}
			flush()
			split = true
		default:
			buf.WriteByte(c)
		}
	}

	if quote != 0 || parenDepth != 0 || backtick {
		return nil // unbalanced — caller falls back to exact match
	}
	flush()

	if !split || len(out) < 2 {
		return nil
	}
	return out
}

// lastMeaningfulByte returns the last non-whitespace byte in buf, or 0.
func lastMeaningfulByte(buf *strings.Builder) byte {
	s := buf.String()
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c != ' ' && c != '\t' {
			return c
		}
	}
	return 0
}
