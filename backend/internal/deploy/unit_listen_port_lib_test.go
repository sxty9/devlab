package deploy

// setup_unit_listen_port reads the loopback port a delivered unit's ExecStart binds. Both the honest
// running gate on production (devlab-deploy-recv) and — through its Go twin DeliveredUnitPort — the dev
// gate must dial the port the UNIT binds, so the two ExecStart forms that occur across the build kinds
// are read here: the go-daemon template's `--listen 127.0.0.1:<port>` and a python-app's uvicorn
// `--port <port>` / `--port=<port>`. Pinned in the shared library so the shell and Go readers agree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibUnitListenPort(t *testing.T) {
	cases := []struct {
		name string
		exec string
		want string
	}{
		{"go-daemon loopback bind", "ExecStart=/opt/x/bin/xd --listen 127.0.0.1:8770", "8770"},
		{"python --port with a space", "ExecStart=/opt/x/venv/bin/uvicorn app:app --host 127.0.0.1 --port 8770", "8770"},
		{"python --port=equals", "ExecStart=/opt/x/venv/bin/uvicorn app:app --port=8791", "8791"},
		{"loopback bind wins over a stray flag", "ExecStart=/opt/x/xd --listen 127.0.0.1:8770 --metrics-port 9000", "8770"},
		{"no port at all", "ExecStart=/opt/x/bin/xd --serve", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "u.service")
			if err := os.WriteFile(f, []byte("[Service]\n"+c.exec+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res := sourceLib(t, nil, "setup_unit_listen_port "+f)
			if got := strings.TrimSpace(res.out); got != c.want {
				t.Errorf("setup_unit_listen_port = %q, want %q", got, c.want)
			}
		})
	}
}
