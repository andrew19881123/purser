//! Explicit node lifecycle state machine.
//!
//! The agent's place in the fleet is a small, well-defined set of states with
//! guarded transitions. Modelling it as an explicit type — rather than an
//! ad-hoc `NodeState` field poked from many places — keeps every state change
//! auditable and makes illegal transitions a compile-adjacent bug we catch at
//! the single choke point ([`NodeStateMachine::transition`]).
//!
//! The happy path is:
//!
//! ```text
//! PROVISIONING → ENROLLED → READY → LOADING → RUNNING
//!                                     ↑           │
//!                                     └── DEGRADED ┘   (crash → restart)
//! ```
//!
//! any live state may enter `DRAINING` (→ `DECOMMISSIONED`), and `UNREACHABLE`
//! is *transversal*: it can be entered from any live state on heartbeat loss and
//! [`recover`](NodeStateMachine::recover)ed back to whatever the node was doing.
//! `DECOMMISSIONED` is terminal.
//!
//! The state values themselves are the shared [`NodeState`] proto enum, so what
//! the machine reports and what goes on the wire are one and the same.

use std::fmt;

use purser_proto::v1::NodeState;

/// A refused state transition — `from` is not allowed to move to `to`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TransitionError {
    /// The state the machine was in.
    pub from: NodeState,
    /// The state that was illegally requested.
    pub to: NodeState,
}

impl fmt::Display for TransitionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "illegal node state transition {} -> {}",
            self.from.as_str_name(),
            self.to.as_str_name()
        )
    }
}

impl std::error::Error for TransitionError {}

/// Guards and records the node's lifecycle state.
///
/// Cheap to hold behind a mutex; every field is `Copy`.
#[derive(Debug, Clone)]
pub struct NodeStateMachine {
    current: NodeState,
    /// Last *live* state, so [`recover`](Self::recover) can restore what the
    /// node was doing before it went `UNREACHABLE`.
    resume: NodeState,
}

impl Default for NodeStateMachine {
    fn default() -> Self {
        Self::new()
    }
}

impl NodeStateMachine {
    /// A freshly-provisioned node that has not yet enrolled.
    pub fn new() -> Self {
        Self {
            current: NodeState::Provisioning,
            resume: NodeState::Ready,
        }
    }

    /// Start the machine in an explicit state (e.g. a node that re-enrolls with a
    /// known identity boots straight into `ENROLLED`).
    pub fn starting_at(state: NodeState) -> Self {
        Self {
            current: state,
            resume: if Self::is_live(state) {
                state
            } else {
                NodeState::Ready
            },
        }
    }

    /// The current state.
    pub fn current(&self) -> NodeState {
        self.current
    }

    /// Whether the node has reached a terminal state and will not transition
    /// again.
    pub fn is_terminal(&self) -> bool {
        self.current == NodeState::Decommissioned
    }

    /// Whether a move to `to` is permitted from the current state.
    pub fn can_transition(&self, to: NodeState) -> bool {
        Self::is_valid(self.current, to)
    }

    /// Attempt a guarded transition, returning the new state on success.
    ///
    /// A no-op transition (`to == current`) is always allowed and idempotent.
    pub fn transition(&mut self, to: NodeState) -> Result<NodeState, TransitionError> {
        if !Self::is_valid(self.current, to) {
            return Err(TransitionError {
                from: self.current,
                to,
            });
        }
        // Remember the last live state so UNREACHABLE can be undone cleanly.
        if to != NodeState::Unreachable && Self::is_live(to) {
            self.resume = to;
        }
        self.current = to;
        Ok(self.current)
    }

    // ---- Convenience transitions (named for the events that drive them) -----

    /// PROVISIONING → ENROLLED, once `RegistrationService::Join` succeeds.
    pub fn enrolled(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Enrolled)
    }

    /// → READY, idle and schedulable.
    pub fn ready(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Ready)
    }

    /// READY → LOADING, an engine is starting.
    pub fn loading(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Loading)
    }

    /// LOADING → RUNNING, the engine reached READY and is serving.
    pub fn running(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Running)
    }

    /// → DEGRADED, the engine crashed or is otherwise impaired.
    pub fn degraded(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Degraded)
    }

    /// → DRAINING, quiescing ahead of maintenance.
    pub fn draining(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Draining)
    }

    /// DRAINING → DECOMMISSIONED, terminal.
    pub fn decommissioned(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Decommissioned)
    }

    /// Transversal: mark the node UNREACHABLE (heartbeat loss). The prior live
    /// state is remembered for [`recover`](Self::recover).
    pub fn unreachable(&mut self) -> Result<NodeState, TransitionError> {
        self.transition(NodeState::Unreachable)
    }

    /// UNREACHABLE → whatever the node was doing before it went silent.
    pub fn recover(&mut self) -> Result<NodeState, TransitionError> {
        debug_assert_eq!(self.current, NodeState::Unreachable);
        self.transition(self.resume)
    }

    /// States in which the node is alive and doing (or ready to do) work.
    fn is_live(state: NodeState) -> bool {
        matches!(
            state,
            NodeState::Enrolled
                | NodeState::Ready
                | NodeState::Loading
                | NodeState::Running
                | NodeState::Degraded
                | NodeState::Draining
        )
    }

    /// The transition matrix. Kept as one explicit function so the whole legal
    /// graph is readable in one place.
    fn is_valid(from: NodeState, to: NodeState) -> bool {
        use NodeState::*;

        // Idempotent self-transition.
        if from == to {
            return true;
        }
        // Terminal: nothing leaves DECOMMISSIONED.
        if from == Decommissioned {
            return false;
        }
        // Unspecified is never a legitimate source or destination.
        if from == Unspecified || to == Unspecified {
            return false;
        }
        // Transversal: any live state may go UNREACHABLE.
        if to == Unreachable {
            return true;
        }
        // Recovery: UNREACHABLE may return to any live state.
        if from == Unreachable {
            return Self::is_live(to);
        }

        matches!(
            (from, to),
            // Enrollment.
            (Provisioning, Enrolled)
                | (Enrolled, Ready)
                // Engine lifecycle.
                | (Ready, Loading)
                | (Loading, Running)
                | (Loading, Ready)     // load aborted / stopped before ready
                | (Loading, Degraded)  // load failed
                | (Running, Ready)     // engine stopped cleanly
                | (Running, Degraded)  // engine crashed
                | (Degraded, Running)  // recovered with a live engine
                | (Degraded, Ready)    // recovered, now idle
                | (Degraded, Loading)  // restart: reloading
                // Drain / decommission.
                | (Ready, Draining)
                | (Running, Draining)
                | (Loading, Draining)
                | (Degraded, Draining)
                | (Draining, Decommissioned)
                | (Draining, Ready)    // drain cancelled
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn happy_path_is_permitted() {
        let mut sm = NodeStateMachine::new();
        assert_eq!(sm.current(), NodeState::Provisioning);
        sm.enrolled().unwrap();
        sm.ready().unwrap();
        sm.loading().unwrap();
        sm.running().unwrap();
        // crash → degraded → reload → running
        sm.degraded().unwrap();
        sm.loading().unwrap();
        sm.running().unwrap();
        // drain → decommission
        sm.draining().unwrap();
        sm.decommissioned().unwrap();
        assert!(sm.is_terminal());
    }

    #[test]
    fn illegal_transitions_are_refused() {
        // Can't run before loading.
        let mut sm = NodeStateMachine::new();
        let err = sm.running().unwrap_err();
        assert_eq!(err.from, NodeState::Provisioning);
        assert_eq!(err.to, NodeState::Running);

        // Can't skip straight to decommissioned.
        let mut sm = NodeStateMachine::starting_at(NodeState::Ready);
        assert!(sm.transition(NodeState::Decommissioned).is_err());

        // Terminal state has no exits.
        let mut sm = NodeStateMachine::starting_at(NodeState::Draining);
        sm.decommissioned().unwrap();
        assert!(sm.ready().is_err());
        assert!(sm.unreachable().is_err());
    }

    #[test]
    fn self_transition_is_idempotent() {
        let mut sm = NodeStateMachine::starting_at(NodeState::Running);
        assert_eq!(sm.transition(NodeState::Running).unwrap(), NodeState::Running);
    }

    #[test]
    fn unreachable_is_transversal_and_recovers_prior_state() {
        let mut sm = NodeStateMachine::starting_at(NodeState::Ready);
        sm.loading().unwrap();
        sm.running().unwrap();
        // Heartbeat loss from RUNNING.
        sm.unreachable().unwrap();
        assert_eq!(sm.current(), NodeState::Unreachable);
        // Recovery returns to RUNNING (what it was doing).
        sm.recover().unwrap();
        assert_eq!(sm.current(), NodeState::Running);
    }

    #[test]
    fn unreachable_from_loading_recovers_to_loading() {
        let mut sm = NodeStateMachine::starting_at(NodeState::Ready);
        sm.loading().unwrap();
        sm.unreachable().unwrap();
        sm.recover().unwrap();
        assert_eq!(sm.current(), NodeState::Loading);
    }

    #[test]
    fn unspecified_is_never_valid() {
        assert!(!NodeStateMachine::is_valid(
            NodeState::Ready,
            NodeState::Unspecified
        ));
        assert!(!NodeStateMachine::is_valid(
            NodeState::Unspecified,
            NodeState::Ready
        ));
    }
}
