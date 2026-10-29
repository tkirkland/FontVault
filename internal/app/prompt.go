// Package app orchestrates fontvault runs: preflight, settings
// confirmation, the four modes, reports and housekeeping.
package app

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// prompter reads interactive answers from stdin.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(stdin io.Reader, stdout io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(stdin), out: stdout}
}

// ask prints prompt and reads a single line, trimmed.
func (p *prompter) ask(prompt string) (string, error) {
	if _, err := fmt.Fprint(p.out, prompt); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	line, err := p.in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// confirm asks a [y/N] question. It returns false on EOF, because the
// non-TTY-without--yes check already guarantees an interactive reader here.
func (p *prompter) confirm(prompt string) (bool, error) {
	line, err := p.ask(prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// choice asks a question with single-letter answers and returns the
// lowercased answer. Invalid answers are re-asked.
func (p *prompter) choice(prompt string, valid map[string]string) (string, error) {
	for {
		line, err := p.ask(prompt)
		if err != nil {
			return "", err
		}
		answer := strings.ToLower(line)
		if _, ok := valid[answer]; ok {
			return answer, nil
		}
		if _, err := fmt.Fprintf(p.out, "  Please answer one of: %s\n", strings.Join(mapKeys(valid), ", ")); err != nil {
			return "", fmt.Errorf("write prompt: %w", err)
		}
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
