Codex-only adapter of github.com/coder/websocket v1.8.14, copied from the locked Go module.

Original license: LICENSE.txt. Other providers retain the original dependency.

Local changes: opt-in client_max_window_bits offer and validation; bounded-window
compression with the already locked klauspost/compress implementation; narrow
window writers never enter the default 32 KiB writer pool. Cleanup error handling
is explicit for the host lint rules; pooled readers and buffers validate their
type before reuse. No protocol parser behavior is otherwise changed.
