package cli

import "strings"

func (f operationFlags) command(args ...string) string {
	args = append(args, "--target", f.target)
	if f.configDir != "" {
		args = append(args, "--config-dir", f.configDir)
	}
	return shellCommand(args)
}

func shellCommand(args []string) string {
	words := make([]string, 1, len(args)+1)
	words[0] = "brine"
	for _, arg := range args {
		words = append(words, shellWord(arg))
	}
	return strings.Join(words, " ")
}

func shellWord(word string) string {
	if word != "" && strings.IndexFunc(word, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) == -1 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
}
