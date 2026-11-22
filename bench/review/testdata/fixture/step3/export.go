package inventory

import "os/exec"

// Export writes the lines of items matching pattern from the stock file to the report file,
// with the system's grep.
func Export(stockFile, pattern, report string) error {
	command := exec.Command("sh", "-c", "grep '"+pattern+"' "+stockFile+" > "+report)
	return command.Run()
}
