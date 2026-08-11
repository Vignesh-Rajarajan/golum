package harness

import "fmt"

type BusyError struct{ Lane, OperationID, OperationKind string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("lane %s busy with %s operation %s", e.Lane, e.OperationKind, e.OperationID)
}

type NoActiveRunError struct{ Lane string }

func (e *NoActiveRunError) Error() string { return "no active run on lane " + e.Lane }

type NoActiveOperationError struct{ Lane string }

func (e *NoActiveOperationError) Error() string { return "no active operation on lane " + e.Lane }

type NothingToResumeError struct{ Lane string }

func (e *NothingToResumeError) Error() string { return "nothing to resume on lane " + e.Lane }

type NothingToCompactError struct{ Lane string }

func (e *NothingToCompactError) Error() string { return "nothing to compact on lane " + e.Lane }

type InvalidMessageError struct{ Lane, Reason string }

func (e *InvalidMessageError) Error() string {
	return "invalid message on lane " + e.Lane + ": " + e.Reason
}

type UnknownSkillError struct{ Name string }

func (e *UnknownSkillError) Error() string { return "unknown skill: " + e.Name }

type UnknownTemplateError struct{ Name string }

func (e *UnknownTemplateError) Error() string { return "unknown template: " + e.Name }

type UnknownTargetError struct{ TargetID string }

func (e *UnknownTargetError) Error() string { return "unknown target: " + e.TargetID }

type UnknownQueueItemError struct{ Lane, EntryID string }

func (e *UnknownQueueItemError) Error() string {
	return "unknown queue item " + e.EntryID + " on lane " + e.Lane
}

type ClosedError struct{}

func (*ClosedError) Error() string { return "harness closed" }

type HarnessFault struct{ Err error }

func (e *HarnessFault) Error() string { return "harness fault: " + e.Err.Error() }
func (e *HarnessFault) Unwrap() error { return e.Err }
