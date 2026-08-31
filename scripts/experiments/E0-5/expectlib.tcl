# E0-5 expect helpers.
#
# THE BUG THIS EXISTS TO FIX. expect only reads the pty while an `expect`
# command is running. A wait loop built from `sleep` and `file exists` never
# reads, so the interactive TUI -- which redraws constantly -- fills the 64 KB
# pty buffer and then BLOCKS ON WRITE. A blocked `claude` stops processing
# input entirely: typed prompts are never submitted and messages posted to its
# inbox socket are never acted on. The session looks alive (its pid is there,
# its hooks already ran) and is in fact frozen.
#
# It also explains why a `sleep`-based hold followed by `send "/exit\r"` and
# `expect eof` still worked: the `expect eof` finally drains the buffer, the
# process unblocks and only then processes the queued keystrokes.
#
# So every wait below is built out of `expect`, which drains.

proc nap {secs} {
    global timeout
    set old $timeout
    set timeout 1
    set end [expr {[clock milliseconds] + int($secs * 1000)}]
    while {[clock milliseconds] < $end} {
        expect {
            -re {(.|\n)+} { }
            timeout { }
            eof { break }
        }
    }
    set timeout $old
}

# Wait for a file to appear, draining the pty the whole time. Returns the
# seconds waited, or -1 on timeout.
proc waitfile {path secs} {
    set end [expr {[clock milliseconds] + int($secs * 1000)}]
    set t0 [clock milliseconds]
    while {[clock milliseconds] < $end} {
        if {[file exists $path]} { return [expr {([clock milliseconds] - $t0) / 1000}] }
        nap 1
    }
    if {[file exists $path]} { return [expr {([clock milliseconds] - $t0) / 1000}] }
    return -1
}
