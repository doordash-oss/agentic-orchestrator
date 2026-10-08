# Supervisor background work

The Background work panel above the composer keeps confirmed scheduled monitors,
event monitors, and provider-reported tasks visible after a reply finishes. The
composer and sidebar summarize watching monitors, running tasks, and tasks that
need attention. Expand a task to see its schedule, last confirmation, and up to
eight meaningful activity updates. Completed and stopped tasks move to Recent
activity; the server includes up to twenty recent terminal tasks.

Ask to stop sends a visible supervisor message (or queues it during a turn),
without changing the current draft. Status changes only after provider
acknowledgement. Failed stop requests leave the task visible.

The server derives the registry from committed transcript records at startup and
on append. It is independent of transcript pagination and survives UI reloads.
Provider IDs are scoped to the process generation. After a process ends or is
replaced, previous active tasks show Interrupted; they are not automatically
restarted. A successful CronList can confirm schedules in the new process.

Task lifecycle events are provider-neutral. Scheduled and event monitors also
recognize successful, correlated Claude CronCreate, CronList, CronDelete, Monitor,
and TaskStop results. Assistant text, failed tool calls, and unrecognized result
formats never establish active work. Individual cron checks and precise next-fire
times are not exposed by those results, so the panel does not invent them. A
reported schedule expiry bounds its active state on the next state read.

Verification covers turn completion, streamed state, paging, reload, generation
changes, expiry, failed and confirmed cancellation, renderer status, and a packaged
Electron journey through creation, conversation, UI reload, cancellation and
restart.
