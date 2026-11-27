package watch

// followedSink is Muse Code's delivery (config.DeliverFollowedSink; card
// 88): the watcher appends each frame to a sink file under the session's
// inbox directory, and the model's monitor follows it with `brigade inbox
// follow`. The hook names the file (`--sink`), so everything here is the
// plain form: an append to a file that grows. Antigravity CLI's delivery
// (config.DeliverNextCall, work item 532) is the same file, which the
// host's own hook reads at the next model call (inbound.DrainNew).
type followedSink struct{ socket }
