use client_contracts::clientui::{
    CONVERSATION_FRESHNESS_ESTABLISHED, CONVERSATION_FRESHNESS_FRESH, RunLifecycle,
    RunLifecycleError, RunLifecyclePhase, RunMode,
};

#[test]
fn conversation_freshness_constants_match_go_wire_values() {
    assert_eq!(CONVERSATION_FRESHNESS_FRESH, 0);
    assert_eq!(CONVERSATION_FRESHNESS_ESTABLISHED, 1);
}

#[test]
fn run_lifecycle_helpers_validate_representable_go_combinations() {
    let idle = RunLifecycle::idle();
    assert!(idle.validate().is_ok());
    assert!(!idle.is_running());
    assert!(!idle.is_finished());
    assert!(!idle.is_goal_loop_running());

    let running_blank = RunLifecycle::running(RunMode::None);
    assert!(running_blank.validate().is_ok());
    assert_eq!(running_blank.mode, RunMode::None);
    assert!(running_blank.is_running());
    assert!(!running_blank.is_goal_loop_running());

    let finished_blank = RunLifecycle::finished(RunMode::None);
    assert!(finished_blank.validate().is_ok());
    assert_eq!(finished_blank.mode, RunMode::None);
    assert!(finished_blank.is_finished());

    let goal = RunLifecycle::running(RunMode::GoalLoop);
    assert!(goal.validate().is_ok());
    assert!(goal.is_goal_loop_running());

    let invalid_idle = RunLifecycle {
        phase: RunLifecyclePhase::Idle,
        mode: RunMode::GoalLoop,
    };
    assert_eq!(
        invalid_idle.validate(),
        Err(RunLifecycleError::IdleWithRunMode)
    );
}
