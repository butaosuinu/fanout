package backend

// AttachExec is the complete process image a plain terminal execs to enter a
// runtime-owned session in place: the runtime's pinned native client, its
// argv, and the full environment the client needs to route to the owned
// server. The caller replaces its own process with this image and never sees
// control again; a caller that cannot exec (no terminal, or the exec itself
// fails) prints the equivalent shell command instead.
type AttachExec struct {
	// Path is the absolute path of the pinned client binary to exec.
	Path string
	// Argv is the complete argument vector, argv[0] included.
	Argv []string
	// Env is the complete environment for the new process image.
	Env []string
}
