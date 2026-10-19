package ops

import (
	"fmt"
	"os/exec"
)

// ResizeImage scales the uploaded image to the requested size.
func ResizeImage(path, size string) error {
	out, err := exec.Command("convert", path, "-resize", size, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize: %w", err)
	}
	_ = out
	return nil
}