// SPDX-License-Identifier: Apache-2.0

import Darwin

/// The CPU time the calling thread spends running body: what the work costs
/// the thread (the main thread, in these tests, at the user-interactive QoS
/// the app's main thread has), without the time a loaded machine kept it
/// waiting for a core. A budget like "under a frame" is about the work the
/// main thread does; wall-clock time on a busy test machine also counts
/// every other process, which failed these tests under load (a 16 ms find
/// chunk measured 34 to 50 ms of wall-clock time for 5 to 7 ms of CPU time)
/// without the code doing any more work. Work handed to another thread
/// isn't counted, and doesn't hold the main thread either.
func threadCPUTime(_ body: () throws -> Void) rethrows -> Duration {
    let start = clock_gettime_nsec_np(CLOCK_THREAD_CPUTIME_ID)
    try body()
    let end = clock_gettime_nsec_np(CLOCK_THREAD_CPUTIME_ID)
    return .nanoseconds(Int64(end - start))
}
