package core

import (
	"fmt"
	"unicode/utf8"
)

// Reject malformed policy instead of truncating it or falling back to user prose.
func ValidateDeveloperInstructions(instructions string) error {
	if len(instructions) > 32*1024 || !utf8.ValidString(instructions) {
		return fmt.Errorf("invalid runtime developer instructions: expected UTF-8 up to 32 KiB")
	}
	return nil
}
