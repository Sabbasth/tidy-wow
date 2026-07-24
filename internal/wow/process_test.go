package wow

import "testing"

func TestIsRunningProcessList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "classic macOS", output: "/Applications/World of Warcraft/_classic_era_/World of Warcraft Classic.app/Contents/MacOS/World of Warcraft Classic\n", want: true},
		{name: "retail macOS", output: "/Applications/World of Warcraft/_retail_/World of Warcraft.app/Contents/MacOS/World of Warcraft\n", want: true},
		{name: "launcher only", output: "/Applications/World of Warcraft/World of Warcraft Launcher.app/Contents/MacOS/World of Warcraft Launcher\n", want: false},
		{name: "unrelated", output: "/usr/bin/login\n/Applications/Battle.net.app/Contents/MacOS/Battle.net\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRunningProcessList(tt.output); got != tt.want {
				t.Errorf("isRunningProcessList() = %t, want %t", got, tt.want)
			}
		})
	}
}
