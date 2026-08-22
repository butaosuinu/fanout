package backend

// AttachExec is the complete process image used to enter a runtime-owned
// session: the runtime's pinned native client, its argv, and the full
// environment needed to route to the owned server. A plain terminal may exec
// it in place; a resident TUI may suspend itself while the process runs.
type AttachExec struct {
	// Path is the absolute path of the pinned client binary to exec.
	Path string
	// Argv is the complete argument vector, argv[0] included.
	Argv []string
	// Env is the complete environment for the new process image.
	Env []string
}
