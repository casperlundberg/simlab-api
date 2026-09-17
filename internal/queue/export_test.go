package queue

import "fmt"

// CheckOrder reports the first place a level is out of deadline order, or a
// job is in the index but in no level, or in two. Empty means none.
func (s *Simulator) CheckOrder() string {
	seen := map[*queued]bool{}
	for priority, level := range s.levels {
		for i, item := range level {
			if seen[item] {
				return fmt.Sprintf("job %d is waiting twice", item.job.ID)
			}
			seen[item] = true
			if item.job.Priority != priority {
				return fmt.Sprintf("job %d holds priority %d but waits at %d", item.job.ID, item.job.Priority, priority)
			}
			if i > 0 && before(item, level[i-1]) {
				return fmt.Sprintf("level %d: job %d (from %v) is behind job %d (from %v)",
					priority, item.job.ID, item.deadlineFrom, level[i-1].job.ID, level[i-1].deadlineFrom)
			}
		}
	}
	for _, item := range s.jobs {
		if item.waiting != seen[item] {
			return fmt.Sprintf("job %d is marked waiting=%v but is in a level=%v", item.job.ID, item.waiting, seen[item])
		}
	}
	return ""
}
