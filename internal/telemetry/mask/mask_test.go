package mask

import (
	"strings"
	"testing"
)

// Synthetic secrets only (no real one).
const (
	hexKey    = "3f9c2a7b1d4e6f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708"
	ghToken   = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	headHex   = "0123456789abcdef0123456789abcdef01234567"
	base64ish = "dGhpcyBpcyBhIHNlY3JldCBrZXkgdmFsdWUxMjM0NTY3OA"
)

func TestCommandMasked(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git reset --hard HEAD~1", "git reset --hard HEAD~1"},
		{"curl -u user:pw https://api", "curl -u *** https://api"},
		{"sshpass -p s3cr3t ssh host", "sshpass -p *** *** ***"},
		{"mysql -ps3cr3t db", "mysql *** ***"},
		{"curl -H 'X-Api-Key: " + hexKey + "' https://api", "curl -H *** https://api"},
		{"codex exec 'Review the plan'", "codex exec <17 bytes>"},
		{"jst bg --for 47 -- codex exec 'p'", "jst bg --for 47 -- codex exec <3 bytes>"},
		{"sed -i '' 's/old/confidential/' note.txt", "sed -i '' <21 bytes> note.txt"},
		{"printf '%s' 'private' >note.txt", "printf <4 bytes> <9 bytes> >note.txt"},
		{"echo hello world", "echo <5 bytes> <5 bytes>"},
		{"curl --data status=confidential https://api", "curl --data *** https://api"},
		{"curl --data=status=x https://api", "curl --data=*** https://api"},
		{"git commit -m 'secret plan'", "git commit -m <13 bytes>"},
		{"gh pr create --title T --body 'long body'", "gh pr create --title <1 bytes> --body <11 bytes>"},
		{"GITHUB_TOKEN=" + ghToken + " gh api user", "GITHUB_TOKEN=*** gh api user"},
		{"env API_KEY=abc ./run", "env API_KEY=*** ./run"},
		{"git -c http.extraheader='AUTHORIZATION: basic xyz' fetch", "git -c http.extraheader=*** fetch"},
		{"tool --token=abc --verbose", "tool --token=*** --verbose"},
		{"tool --password abc", "tool --password ***"},
		{"git clone https://user:pw@host/repo.git", "git clone ***"},
		{"git checkout " + headHex, "git checkout ***"},
		{"sh -c 'echo secret'", "sh -c 'echo <6 bytes>'"},
		{"cat <<EOF > f.txt\nprivate contents\nEOF", "cat <<EOF > f.txt\n<20 bytes>"},
		{"python3 -c 'print(1)'", "python3 -c <10 bytes>"},
		{"curl -uuser:hunter2 https://api", "curl *** https://api"},
		{"curl '-HAuthorization: Bearer x' https://api", "curl *** https://api"},
		{"curl -bsession=abc https://api", "curl *** https://api"},
		{"git commit -mprivate", "git commit -m<7 bytes>"},
		{"DATABASE_URL=postgres://alice:hunter2@db/app psql", "DATABASE_URL=*** psql"},
		{"KEY_ID=" + ghToken + " run", "KEY_ID=*** run"},
		{"MODE=fast make", "MODE=fast make"},
		{"curl -suuser:hunter2 https://api", "curl *** https://api"},
		{"curl -su user:hunter2 https://api", "curl *** *** https://api"},
		{"curl -sSLH 'Authorization: Bearer x' https://api", "curl *** *** https://api"},
		{"curl -s -o out.txt https://api", "curl -s -o *** https://api"},
		{"mysql -uroot -phunter2 db", "mysql *** *** ***"},
		{"export DATABASE_URL=postgres://alice:hunter2@db/app", "export DATABASE_URL=***"},
		{"local API_TOKEN=abc", "local API_TOKEN=***"},
		{"declare -x PASSWORD=abc", "declare -x PASSWORD=***"},
		{"URL=https://alice:pw@host; curl $URL", "URL=***; curl ***"},
		{"git commit -am 'private note'", "git commit -am <14 bytes>"},
		{"curl -sxhttp://alice:pw@hub.example https://api", "curl *** https://api"},
		{"curl -o/tmp/job -u alice:hunter2 https://api", "curl *** -u *** https://api"},
		{"ENV=(API_TOKEN=hunter2); env \"${ENV[@]}\" tool", "ENV=***; env <11 bytes> tool"},
		{"ARGS=(--password=hunter2)", "ARGS=***"},
		{"tool --verbose --api-token abc --name x", "tool --verbose --api-token *** --name ***"},
		{"API_TOKENS=(hunter2 other)", "API_TOKENS=***"},
		{"declare -A settings=([API_TOKEN]=hunter2)", "declare -A settings=***"},
		{"CMD=(curl -u alice:hunter2 https://api); \"${CMD[@]}\"", "CMD=***; <11 bytes>"},
		{"docker --config /tmp/docker-auth login -u alice -p hunter2", "docker --config *** *** -u *** -p ***"},
		{"mysql -phunter2= app", "mysql *** ***"},
		{"declare -A settings; settings[API_TOKEN]=hunter2", "declare -A settings; settings[API_TOKEN]=***"},
		{"keytool -list -keystore app.p12 -storepass hunter2", "keytool *** *** *** *** ***"},
		{"sshpass -p hunter2= ssh host", "sshpass -p *** *** ***"},
		{"URLS=(postgres://alice:hunter2@db/app plain)", "URLS=***"},
		{"jq '.private' file.json", "jq <10 bytes> file.json"},
		{"ls internal/telemetry/journal/exporter_something.go", "ls internal/telemetry/journal/exporter_something.go"},
		{"go test ./... | tail -5 && echo done", "go test ./... | tail -5 && echo <4 bytes>"},
		{"/Users/someone/repositories/a/very/long/path/that/is/longer/than/sixty-four/bytes/jst version",
			"/Users/someone/repositories/a/very/long/path/that/is/longer/than/sixty-four/bytes/jst version"},
	}
	for _, testCase := range cases {
		if got := Command(testCase.in, Masked); got != testCase.want {
			t.Errorf("Command(%q)\n got %q\nwant %q", testCase.in, got, testCase.want)
		}
	}
}

func TestCommandSkeleton(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git checkout -- docs/plan.md", "git checkout -- <12 bytes>"},
		{"git reset --hard HEAD~1", "git reset --hard <6 bytes>"},
		{"go test -run=TestX ./internal", "go test <10 bytes> <10 bytes>"},
		{"sshpass -p s3cr3t ssh host", "sshpass -p *** *** ***"},
		{"MODE=fast make", "MODE=<4 bytes> make"},
		{"export MODE=fast", "export MODE=<4 bytes>"},
		{"LIST=(a b)", "LIST=***"},
	}
	for _, testCase := range cases {
		if got := Command(testCase.in, Skeleton); got != testCase.want {
			t.Errorf("Command(%q, skeleton)\n got %q\nwant %q", testCase.in, got, testCase.want)
		}
	}
}

func TestUnparsable(t *testing.T) {
	if got := Command("echo 'unterminated", Masked); got != "<unparsed 18 bytes>" {
		t.Fatalf("got %q", got)
	}
}

func TestArgv(t *testing.T) {
	cases := []struct {
		in, want []string
		mode     Mode
	}{
		{[]string{"--password", "hunter2"}, []string{"--password", "***"}, Masked},
		{[]string{"curl", "--password", "hunter2"}, []string{"curl", "--password", "***"}, Masked},
		{[]string{"jst", "bg", "--for", "47", "--", "codex", "exec", "private prompt"},
			[]string{"jst", "bg", "--for", "47", "--", "codex", "exec", "<14 bytes>"}, Masked},
		{[]string{"git", "status"}, []string{"git", "status"}, Masked},
		{[]string{"curl", "-uuser:hunter2", "https://api"}, []string{"curl", "***", "https://api"}, Masked},
		{[]string{"curl", "-suuser:hunter2", "https://api"}, []string{"curl", "***", "https://api"}, Masked},
		{[]string{"curl", "-sxhttp://alice:pw@hub.example", "https://api"}, []string{"curl", "***", "https://api"}, Masked},
		{[]string{"curl", "-su", "user:hunter2", "https://api"}, []string{"curl", "***", "***", "https://api"}, Skeleton},
		{[]string{"sh", "-c", "curl -u a:b x"}, []string{"sh", "-c", "curl -u *** ***"}, Masked},
		{[]string{"test", "--scope", "gate"}, []string{"test", "--scope", "gate"}, Masked},
	}
	for _, testCase := range cases {
		got := Argv(testCase.in, testCase.mode)
		if strings.Join(got, "\x00") != strings.Join(testCase.want, "\x00") {
			t.Errorf("Argv(%q)\n got %q\nwant %q", testCase.in, got, testCase.want)
		}
	}
}

func TestText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fetch https://user:pw@host/x failed", "fetch https://***@host/x failed"},
		{"Authorization: Bearer abcdefghijk", "Authorization: ***"},
		{"token " + ghToken + " refused", "token *** refused"},
		{"key " + hexKey, "key ***"},
		{"blob " + base64ish, "blob ***"},
		{"API_TOKEN=abc and PATH=/usr/bin", "API_TOKEN=*** and PATH=/usr/bin"},
		{"panic: runtime error at internal/telemetry/journal/exporter.go:42", "panic: runtime error at internal/telemetry/journal/exporter.go:42"},
	}
	for _, testCase := range cases {
		if got := Text(testCase.in); got != testCase.want {
			t.Errorf("Text(%q)\n got %q\nwant %q", testCase.in, got, testCase.want)
		}
	}
	if long := Text(strings.Repeat("a ", 5000)); len(long) > TextLimit+4 {
		t.Fatalf("not cut: %d", len(long))
	}
}
