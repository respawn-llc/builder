package migrationcheck

import (
	"errors"
	"fmt"
	"reflect"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protoapi"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	runpromptpb "core/shared/protoapi/gen/kent/api/run_prompt"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	transcriptpb "core/shared/protoapi/gen/kent/api/transcript"
	"core/shared/protocol"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const reviewedExceptionalWireFingerprint = "532af3e1dabe9bbdef4f96b887eb483e92f781de4c23b398fbf1665b8623847d"

func actualTargetWireExceptions() []WireException {
	return []WireException{
		wireExceptionSignoff[serverapi.OnboardingImportSelection]("kent.api.onboarding.ImportSelection", WireExceptionCustomWire, "e473d132e13439ce788ddeb22a4e3afc904f8d6e8c23a01ead50fa79e2a17f09", "90d4b8acd87ebe1653a30c80790588c9223a498b3d969c3f32538f5b71c8568e"),
		wireExceptionSignoff[serverapi.SessionLaunchIntent]("kent.api.session_launch.SessionLaunchIntent", WireExceptionCustomWire, "e7f701fee4ff7f147ffbb75cd5761737749b546e38c96bd666e46191c5e43207", "c82e848f4bd5dae76c372c2daf0da9c038173a385f206fba54c3ebc989175162"),
		wireExceptionSignoff[serverapi.SessionExecutionEnvironment]("kent.api.session.ExecutionEnvironment", WireExceptionCustomWire, "a231e422b41e62fa1b5f307614a961db5a7490ec2c141e77bb5c8b6b1f32b02a", "d316523023a6d35f938e46044f3070e0c48628e30ccbec7924cef51d7f386712"),
		wireExceptionSignoff[serverapi.AskListPendingBySessionResponse]("kent.api.prompt.ListQuestionsSuccess", WireExceptionFieldReshape, "85425761cf10ee8555ebb1f2e5686d10b1e0f7b618be215239c203e4c8cfee08", "9f24f00e5eb22ee246ec6b2176f45ace3d9d262d943880bac8c042d0f389c250"),
		wireExceptionSignoff[protocol.AttentionNotificationEventParams]("kent.api.attention.NotificationEvent", WireExceptionOneofReshape, "14cb26bba252154f7fa5c4b670538eb7616da092a5d489e736706631fca5b6d9", "412f8ed976baa568a437096e2cc1d26dc99bda41031c3a6ca14fa021031547e7"),
		wireExceptionSignoff[protocol.AttentionNotificationEventParams]("kent.api.workflow_task.AttentionNotificationEvent", WireExceptionOneofReshape, "14cb26bba252154f7fa5c4b670538eb7616da092a5d489e736706631fca5b6d9", "4dfcdab5e13319283103e41cc744b4c65f8b50922b34ee165f52b65d8ae4f033"),
		wireExceptionSignoff[protocol.SubscribeResponse]("google.protobuf.Empty", WireExceptionEmptyAcknowledment, "165aa6664b8c709fbdeff187833c0bca9a5a763089d0bf2ddb6e4f9e319d013b", "d82fa7cd948c455a05d4c5c65b83657f76a3522f168680f4d9dc3c10ea93c9db"),
		wireExceptionSignoff[serverapi.AuthStatusResolution]("kent.api.auth.StatusResolution", WireExceptionOneofReshape, "78e2f96342983a6b2d567af22cec1aa31b4fb0b1484c08451af23ea0de19a305", "0e3011959b5a6012e75658cb7e742aaf1bde11bd6104f2e0dfc0a63f0a7d19f4"),
		wireExceptionSignoff[clientui.BackgroundProcess]("kent.api.process.BackgroundProcess", WireExceptionFieldReshape, "0b019aee0eac99340ebaa733d3a12e385214d987c80bbc96c0597bc4558090bb", "5e0042919ee7e72f9240e63ff2aa1fa48c3ea5f5296f14b2abd7a40d14c3a979"),
		wireExceptionSignoff[serverapi.ProcessInlineOutputResponse]("kent.api.process.InlineOutputSuccess", WireExceptionFieldReshape, "d46ccf50d1b6d3c407a44e57634133bf6057ec634942fde0675726370b542d47", "38e4c7eca0a059944dc8d8a5c1db9151b857c8f7c6dbd18a941bd3371895807a"),
		wireExceptionSignoff[protocol.AttachProjectRequest]("kent.api.connection.AttachProjectRequest", WireExceptionOneofReshape, "f3ecae19f0ed5a21f5926b50baf33b66408f0fe251b5334ddf48b46ec2058455", "9b7e45b7049ad04cf805f98820b6101e727895d1406ecc5cbacef8e352d6fd6c"),
		wireExceptionSignoff[protocol.AttachResponse]("kent.api.connection.AttachmentSuccess", WireExceptionOneofReshape, "7c723fa9dd250aed4fe9f4fb974439013b492749e12728e9c7048afa28b2b3e3", "9496e34dca622acd6c1ca505cadc55aae49609264b7b12e80ba36d569530e35f"),
		wireExceptionSignoff[serverapi.ProjectBinding]("kent.api.project.ProjectMutationBinding", WireExceptionFieldReshape, "981df7ba930bcde24e12ff85b289a7519b5054d30063a6e940230f4b1319b486", "dbe7220dfa19eea46b3b12da84bc74ba631efb3f35e659ce935810b338fa9825"),
		wireExceptionSignoff[serverapi.ProjectDefaultWorkspaceSetRequest]("kent.api.project.SetDefaultWorkspaceRequest", WireExceptionFieldReshape, "d0903f27f14b57a7d749706e660dc5f3fcc4bf526c928d261927a58966ecd8bb", "6cfa2898a0987358aa35b72c785a244b8e2448c3352fd7955017f05f70328b52"),
		wireExceptionSignoff[serverapi.ProjectHomeSummary]("kent.api.project.ProjectHomeSummary", WireExceptionFieldReshape, "b7141a117f6266cf08b28f577de27fdd4cbf3114aa6e84186a933dd90e18f4e9", "bb04fbdeafeeb1c18310b5d92fbb650e21dd40a5dd0fa168cd5a51b3ebdb0ccd"),
		wireExceptionSignoff[serverapi.ProjectWorkspaceSummary]("kent.api.workflow_task.TaskSourceWorkspace", WireExceptionFieldReshape, "35997181b3f5694b65e60c1f2bc2963b2434f4e5f61f5a542a72c2f348ffee4f", "8a255e54849d8d191365d5bcfd6f5ccbffd69c57944b2a7617d801de6e9de689"),
		wireExceptionSignoff[serverapi.ProjectHomeListResponse]("kent.api.project.ProjectHomeListSuccess", WireExceptionFieldReshape, "d1506234e72a62f5378ccbf6ad6180672560c5dd4d0a993c34742881948c873d", "3bb065b2210de86023e1ba1d3e34f1f8d8e1a002465d435170f41794818f7bcf"),
		wireExceptionSignoff[serverapi.ProjectWorkspaceGetRequest]("kent.api.project.GetProjectWorkspaceRequest", WireExceptionOneofReshape, "24fd81cc3d475d8e9572e8278bfef2ff71945620053b299501f8a9017e911bb7", "ede4de23dceaeb429fd3ae69fac96eea0b8bdcd8d504907a840f54702a979503"),
		wireExceptionSignoff[serverapi.ProjectWorkspaceUnlinkRequest]("kent.api.project.UnlinkWorkspaceRequest", WireExceptionFieldReshape, "e395a032d34cbf0f475fd86a39f93d4fa1c2799822f62373c399d222a9299e28", "fd178befc09e891c4e555f542cb6a1d7613f6cd6f30bb8dd0a78415af74c2265"),
		wireExceptionSignoff[protocol.PromptFollowUpEventParams]("kent.api.prompt.FollowUpEvent", WireExceptionFieldReshape, "cac9a46268261f2cb2f00a700af482264ff6a445c80ba5ba5b15d80daf6665db", "47b516bee6246428c18cdb37b86c6f9a4949bff8fd0e00703ff1e4053b705b4e"),
		wireExceptionSignoff[serverapi.RunPromptProgress]("kent.api.run_prompt.ProgressEvent", WireExceptionOneofReshape, "b3db3e2787e6f7433efaf5e10ff0d72377c5511894dd555c17c32a62f7e40ebe", "4d1829873a3cb385f91675df1cde9b056cb7bb833b1913635684bad4730fa302"),
		wireExceptionSignoff[serverapi.RunPromptResponse]("kent.api.run_prompt.Success", WireExceptionFieldReshape, "6a455ce700dd4e28400c184d7b685ccf2288555ea3d0e473e60e27118e97422d", "861bd4f818780f6b8ed073437422b0bf2f1cdeffdd85ed5f1d1021f2ac9e340b"),
		wireExceptionSignoff[serverapi.RuntimeLiveWaitResponse]("kent.api.runtime.LiveWaitSuccess", WireExceptionOneofReshape, "1219a8f4adf8c6913fda169331d7a32ac0381199a627e4f6643d4e7697c7cef3", "c8973268814a042750428b1e87d953a837978136007cfa4c8deaaecc9cb45e3e"),
		wireExceptionSignoff[serverapi.RuntimeLiveWatchOutcome]("kent.api.prompt.LiveWatchOutcome", WireExceptionOneofReshape, "2c1fcb261f58e21765c3ebba2187e963e3cb2c21835a6777138c0c8d25b1ac32", "dcd6360e9d859ef10a2951a58afd2780f5bf88339013c11154e3efab925ebeed"),
		wireExceptionSignoff[serverapi.RuntimeGoalShowResponse]("kent.api.runtime.GoalShowSuccess", WireExceptionFieldReshape, "30aea36c1ebcbbee81b278b17858ad9653d22be144a712945222d7b2641690fa", "eb882fe50da68f7043b2fbec2743ab09f387d2c24d18454bdbada26b1ca5a2e2"),
		wireExceptionSignoff[clientui.RuntimeGoal]("kent.api.runtime.GoalView", WireExceptionFieldReshape, "5d495850fce6a0006c55f0cc9ab085874d4da742fe66ec00930b325132c2d222", "57d6ad969788d140749f35471bde62912ae2e29c8ed6cd5f40727f93b9361a60"),
		wireExceptionSignoff[runtimeinput.Input]("kent.api.runtime.UserTurnInput", WireExceptionFieldReshape, "a5db12285f7fe96a7c42dd5d39fa70d6210f2f7d8dd1d95f38f1d561dae3efeb", "806ae9d4e254a9fb15217bd07fc97412930c1f4ca928ffbb762fede3ffeb5dd2"),
		wireExceptionSignoff[serverapi.RuntimeSubmitUserTurnResponse]("kent.api.runtime.SubmitUserTurnSuccess", WireExceptionOneofReshape, "34de4fdd64b6868dd545697953c53cee3e13b5550456deaa056b93b6eaca0977", "13ce76d533903d301aa8524dab570b7aec1f5702586e8d0522ebf19f83c78000"),
		wireExceptionSignoff[serverapi.ServerReadinessResponse]("kent.api.server.GetReadinessSuccess", WireExceptionFieldReshape, "b2a549921e1c9aa1ef7bb65e70d789149e4a0b3b648d403f664639cdd3fd4eb3", "4ed1763079a0eea5854b0915811b99ab09a3f6d6547f58144e3235d34f9e4887"),
		wireExceptionSignoff[serverapi.UpdateStatusResponse]("kent.api.server.GetUpdateStatusSuccess", WireExceptionFieldReshape, "6e50a015cc4ead280b1b79a8a9f26bf266a6e71ff73b311aed9a0f00fdfa1c9f", "3ac016331ee780bbd02f2cb4d86d5a31d9996b4309624cdf4f1907f3a5aebeec"),
		wireExceptionSignoff[clientui.RuntimeContextUsage]("kent.api.runtime.ContextUsage", WireExceptionFieldReshape, "a332c153bf134bc681c8576ff709315aca28ce9732fbc43457ad51eb51c28fe8", "b8243957306965981069a360040af447a4a3dc457e4bb4a8406a7fbabaa46a78"),
		wireExceptionSignoff[clientui.TranscriptCommittedRow]("kent.api.transcript.CommittedRow", WireExceptionFieldReshape, "5c4cb3eee932c0c7e2b4adc2310d248aa8cb18ec1d0db9870e25534fd2a16902", "22b9eb76cd37d305389af3557488b1dab6790eba446a2facf5db898632876adf"),
		wireExceptionSignoff[rollbacktarget.CandidateLocator]("kent.api.transcript.RollbackCandidate", WireExceptionFieldReshape, "f7f923a3d5d95572ad523819430c4b66f6779ef15405b31b7f96d67606b7940f", "3d73b39754b4df338bda14525d5499a1e00786d5163a1ad8882c6fa06e4984c6"),
		wireExceptionSignoff[config.Settings]("kent.api.session_launch.Settings", WireExceptionFieldReshape, "cebb39125cc5fc4d862a2734c25d69d6b2e2df86814141e6d2fb43725457f64d", "652e3917d3cc251822f7298412ac37d2ef1a076297dffd36535ca5643d2d00a2"),
		wireExceptionSignoff[config.SourceReport]("kent.api.session_launch.SourceReport", WireExceptionFieldReshape, "e3a748894aa163453858ab8cbf5315ad99638c59dba6cbabc887af3d31b1496b", "864e5e9072e1a6dcee2266e148c8b3fa13ebb21088d14bda821249d77bd4789b"),
		wireExceptionSignoff[serverapi.SessionDirective]("kent.api.session_launch.SessionDirective", WireExceptionOneofReshape, "b98c0a7d11cd8e6ef733d560540dca2d7c55516fceacf4ce8397789598eb3e00", "e3faa59023df316af67c75f0e54d6d44139bcf81a3bf94dbbdf570caa5d88d34"),
		wireExceptionSignoff[protocol.SessionTranscriptEventParams]("kent.api.transcript.Message", WireExceptionOneofReshape, "3e8e179b3a8325d8b745e3e0f6f365a86eea68b826543d9f777d5fcf04c436c2", "615b3e337cd4dfb9e26576ac209e424896ef4e2ad66388e5d59481cd8bf251d7"),
		wireExceptionSignoff[serverapi.WorkspaceChatDraftRequest]("kent.api.session_launch.WorkspaceChatDraftRequest", WireExceptionOneofReshape, "f7ff42836dd256dbbccc3b8211365946b6a6e29a4c504e7b8dcabbeb0a3a7a73", "1a6ab66bba0f99a0cc9e8b6f119ff3fd90ab3ebec5682bbb3560acfd25884f76"),
		wireExceptionSignoff[serverapi.WorkflowAttentionListResponse]("kent.api.workflow_task.AttentionListSuccess", WireExceptionFieldReshape, "0bdafdde8a2a6a798774ca203ce80eb19f7c55513e2cca29c1315426c182f390", "24ddfb6a62549d889fe9c6c04b0e90d576aa32bcd021efc9b6e4fef6bb04e217"),
		wireExceptionSignoff[serverapi.WorkflowTaskLabelFilter]("kent.api.workflow_task.LabelFilter", WireExceptionOneofReshape, "6f48e45f62a5064d5229d75e6a452ea08f121792bcdb0230236cc736ffc60c8d", "6ce362cebcf0f863d488ac27ffa9f64cb50b54072866dba11457be6a54241728"),
		wireExceptionSignoff[serverapi.WorkflowBoard]("kent.api.workflow_task.Board", WireExceptionFieldReshape, "414d41793329da8392bb4274f1fe3722670333e0e8cd7954c21767b25542e550", "15a94c5e7cf5ae0151a0f860e0e30844bc924e3e97c6e1cfb48fcfed69eee98c"),
		wireExceptionSignoff[serverapi.WorkflowBoardNodeCardsListResponse]("kent.api.workflow_task.BoardNodeCardsListSuccess", WireExceptionFieldReshape, "35db67e7109a016dcc682ec69e13c22f6c11ab92adb21a761585b65293057be2", "7a67d6af4a16d2176dd599525b746371b7928137e6beec4097c7975d4a7fecb4"),
		wireExceptionSignoff[protocol.WorkflowProjectEventParams]("kent.api.workflow_definition.ProjectEvent", WireExceptionFieldReshape, "0da93c2e200cf283e9cd2a2064ea0eb303cbaf35077dacd0aa1d1a25045b99e8", "1e061a7ec6cfac3efeec1c4c1451e4befbb96ece3062a5750adfb8b13db226b4"),
		wireExceptionSignoff[serverapi.WorkflowTaskActivityListResponse]("kent.api.workflow_task.ActivityListSuccess", WireExceptionFieldReshape, "b7b2b2b485f5a9b082a32f397eacdfd06052de22dcae1bb3a9e08062e00b54f6", "9c641379249644575c482c0d91609683a346c86f445ebf7941c2c7689b1b7d9b"),
		wireExceptionSignoff[serverapi.WorkflowTaskApproveResponse]("kent.api.workflow_task.ApproveSuccess", WireExceptionFieldReshape, "b9be459f0f0427762b5037b53b9089f4c9af13f86330a802e8ffc843c4623cb3", "4578f980aaa50bb8a2bddbebb0d499bd647df6722cec47a1514e0568094c0f2a"),
		wireExceptionSignoff[serverapi.WorkflowTaskAttentionListResponse]("kent.api.workflow_task.TaskAttentionListSuccess", WireExceptionFieldReshape, "f31e3567162b0e0ba2c5367757905b9ba142f997ea3b4808f480b0e8887b0d85", "dbe961424af57427037c83a92e1fff3192ec4c620d49ad127f39804f1df55397"),
		wireExceptionSignoff[serverapi.WorkflowTaskComment]("kent.api.workflow_task.Comment", WireExceptionFieldReshape, "db17323a91312bc73b28d3374c5b646fb4e00ee2996df5a55b65cc2a8df6e233", "9220a5eb228803ac761a780fff614ef709f7e0a1b362c5e4104aa8e082d35e0f"),
		wireExceptionSignoff[serverapi.WorkflowTaskCommentListResponse]("kent.api.workflow_task.CommentListSuccess", WireExceptionFieldReshape, "da959845a61c581a3a05b5ce3ecdbdcf0f0f827942135647397cd40513c28b4e", "0b20e2a1259e4084011dd6e430fd4ccea27308ae66e6003c4cab0b16c3f93bee"),
		wireExceptionSignoff[serverapi.WorkflowTaskSummary]("kent.api.workflow_task.TaskSummary", WireExceptionFieldReshape, "6fd3342a1b7a3df8be7196a76241828b2916ba8952b66541288bcfa0ab5cd44a", "338dd295bf256eb91c6636c6c94902f65546ecf39a38d3b8e0fbca0dfb751d9b"),
		wireExceptionSignoff[serverapi.WorkflowTaskListResponse]("kent.api.workflow_task.ListSuccess", WireExceptionFieldReshape, "2443c53e5331c85831c069d7e2826def12f0cc16794387088493185d14160ea5", "73b29c6c6f089a46134de2da7ca43e6feb7cd1deb21c24e08c5ba2bfd2e82901"),
		wireExceptionSignoff[serverapi.WorkflowTaskMovePreviewResponse]("kent.api.workflow_task.MovePreviewSuccess", WireExceptionFieldReshape, "cf0872e7613e2b3cefca7b828f2bb6c92428deaa46cd30f50df03e0f3ab563a2", "4530b08f055552d295b9e02e16063b4e5e8e446d1f908a4e0580c1917f251d1d"),
		wireExceptionSignoff[serverapi.WorkflowTaskMoveResponse]("kent.api.workflow_task.MoveSuccess", WireExceptionOneofReshape, "c1ccf0a0b5b7cd47f1d877fb33f8c8014a7ce159040e4d0adf3b7d5615e8b989", "18b6605421151ac40daeab8bf2d6ad762f26c1880bdde3aaf4d2a845d2e67202"),
		wireExceptionSignoff[serverapi.WorkflowTaskObservationOutcome]("kent.api.workflow_task.ObserveOutcome", WireExceptionOneofReshape, "e15c0b920ece9ff97957622aee9f8389664528315799c4a3841c8bc6255d2655", "bca88c47c264b58a6e6d3fe1f2a36f5e27f2a1944a50b1e0d62ee9d7040eb348"),
		wireExceptionSignoff[serverapi.WorkflowTaskResumeResponse]("kent.api.workflow_task.ResumeSuccess", WireExceptionFieldReshape, "b5c406ede0ab70b4cc0de3e2c461fddbff9aa0432d67ee819752414248789797", "32fb1c944a01dd46af1d3005c501e13b01b1e593ff6ae49d6134dd83e3890bef"),
		wireExceptionSignoff[serverapi.WorkflowTaskSessionListResponse]("kent.api.workflow_task.SessionListSuccess", WireExceptionFieldReshape, "34e5e686cc0b89f23d6d44d2874f05ad5e313dcc0566158d343a58919760d00e", "f7a991a22c653cda90c38977f40d663df626406a6110245d250caefd9cf8faf0"),
		wireExceptionSignoff[serverapi.WorkflowTaskStartResponse]("kent.api.workflow_task.StartSuccess", WireExceptionOneofReshape, "2afe5ddf0322d95b9b8e830ed7da24598d1bcec462a1acc9b1535159ce0149d9", "49c28bdee2b15b4f5a91a3be40fe3ad6767895423b4d9e6a6c71a1703198e490"),
		wireExceptionSignoff[serverapi.WorkflowGraphSaveResponse]("kent.api.workflow_definition.GraphSaveSuccess", WireExceptionCollectionReshape, "c43de89e79754ff7eaf4bcf5bb02c835347018a83e6292f667adfd050bcb006b", "dbe5ff0999bf0fbf57f278d70f26ecd283e98afd5c26638bc0f67bf725634b5a"),
		wireExceptionSignoff[serverapi.WorkflowGraphSavePreviewResponse]("kent.api.workflow_definition.GraphSavePreviewSuccess", WireExceptionCollectionReshape, "da536153841a28eaf639bbece22d693c9c8c97edfe6b761ca5aaa4b3a600b97e", "f78f22c2f2ef692b0a13c45bf7c3b60d6a2c9f439e454c0cc2b270c68e3ab8d1"),
		wireExceptionSignoff[serverapi.WorkflowGraphValidateDraftResponse]("kent.api.workflow_definition.GraphValidateDraftSuccess", WireExceptionCollectionReshape, "cd40e9999fe8485610e1dbef1fea0cd590003e89ccc6f1e8a589196c7b3fb471", "283b7c2f23a0d278c3d8c348a1e5eca88da973b85c432b9d6678c573c9297a5d"),
		wireExceptionSignoff[serverapi.WorkflowTaskCompleteRequest]("kent.api.workflow_task.CompleteRequest", WireExceptionCollectionReshape, "e4d073345d4cae884c3c795188c9163097353e5b0447005e97f3e676a7b75060", "fe9c8bff305bc3631383fde6e108da4dac645fcad236399261d554b45a974c0d"),
		wireExceptionSignoff[serverapi.WorkflowTaskMoveRequest]("kent.api.workflow_task.MoveRequest", WireExceptionCollectionReshape, "77280238168f08334652641f17a0cd0eb45c38bd5e3fb6a23abd51cc08308894", "fc6ce1a920ea4123cd3e5470e07bda6359a1d3e66d20bd0373ba6b120ef962d8"),
		wireExceptionSignoff[serverapi.WorktreeTopologyEntry]("kent.api.worktree.TopologyEntry", WireExceptionOneofReshape, "dec2dedb7f1220a5b64c611694028d2c98a83349d8aba3efc984fb210384a251", "cc5050e0a4463f623c0f0390dd0fb7bf9d802043673d66c7a34812d6ac5a2862"),
		wireExceptionSignoff[serverapi.WorktreeDeleteRequest]("kent.api.worktree.DeleteRequest", WireExceptionFieldReshape, "e591cfd3b02347b9cc098b981acb9b6f134d4824b7a95637063373addfa4c548", "8c680f7db293c6088bc7dca8b1301c535cec6a29bf8ad8c6daa65dc26e035e49"),
		wireExceptionSignoff[serverapi.WorktreeDeleteResult]("kent.api.worktree.DeleteSuccess", WireExceptionFieldReshape, "23874bdd52e1a582609270cc1016fb1120ca65a591e536b88bb8a93824b1a1b4", "48ad6682ea4f86518039dfd1d22f81db2120f8a41acd1745d9deeb76b1c0ae62"),
		wireExceptionSignoff[serverapi.WorktreeEnterRequest]("kent.api.worktree.EnterRequest", WireExceptionFieldReshape, "a0f130be369436c2d0c9b23bf0eb65b8c4118e498dbc8bc816316b2cb5ed4f09", "993589cf55620531c271e7bda58cac686ca7a9e88afc95b8a59464588ebec62e"),
		wireExceptionSignoff[serverapi.WorktreeLeaveRequest]("kent.api.worktree.LeaveRequest", WireExceptionFieldReshape, "ada2e2d635da350049bfbdd29d72e8b73fdf8f0ada904b0ce482745ae1bdb6b5", "20a0a210647730cbee2ba1e51277a2582b9ee6347834e80e549251a0123911b4"),
		wireExceptionSignoff[protocol.StreamCompleteParams]("kent.api.worktree.SetupCompletion", WireExceptionFieldReshape, "ec45766d5d031f43b6b72f68b420eaa81d70c43d04433406c417988f4c753b43", "1834eba547236ef20959077e5705ccf23c5eac3537b271635be0800b6e3ca1d9"),
		wireExceptionSignoff[protocol.WorktreeSetupEventParams]("kent.api.worktree.SetupEvent", WireExceptionOneofReshape, "08515f0e3215c776c462aaf31310c36ac4783dcb6c30ee2dafb8174b844beac9", "9a1f920269c6700bbaa9fd0cd2631bea5d378af837835d04309269233fd71b17"),
	}
}

func actualTargetFieldRenames() []WireFieldRename {
	return []WireFieldRename{
		fieldRename[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "OAuthCodeVerifier", "oauth_code_verifier"),
		fieldRename[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "OAuthState", "oauth_state"),
		fieldRename[serverapi.AuthGetBootstrapStatusResponse]("kent.api.auth.BootstrapStatus", "OAuth", "oauth"),
		fieldRename[serverapi.AuthProviderSelection]("kent.api.auth.ProviderSelection", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.AuthProviderCapabilitySelection]("kent.api.auth.ProviderCapabilitySelection", "IsOpenAIFirstParty", "is_openai_first_party"),
		fieldRename[serverapi.AuthSubscriptionWindowFacts]("kent.api.auth.SubscriptionWindowFacts", "DurationSecs", "duration_seconds"),
		fieldRename[serverapi.CapabilityFactsRequest]("kent.api.capability.GetFactsRequest", "ExplicitLLMProviderIDs", "explicit_llm_provider_ids"),
		fieldRename[serverapi.LLMProviderCapabilityFact]("kent.api.capability.ProviderFact", "IsOpenAIFirstParty", "is_openai_first_party"),
		fieldRename[serverapi.OnboardingProviderChoice]("kent.api.onboarding.ProviderChoice", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.RunPromptOverrides]("kent.api.session_launch.RunPromptOverrides", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.SessionPlan]("kent.api.session_launch.SessionPlan", "EnabledToolIDs", "enabled_tool_ids"),
		fieldRename[serverapi.SessionRuntimeActivateRequest]("kent.api.session_launch.SessionRuntimeActivateRequest", "EnabledToolIDs", "enabled_tool_ids"),
		fieldRename[serverapi.WorkflowValidationError]("kent.api.workflow_definition.WorkflowValidationError", "RelatedIDs", "related_ids"),
		fieldRename[serverapi.WorkflowProjectLabelReorderRequest]("kent.api.workflow_definition.ProjectLabelReorderRequest", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskCreateRequest]("kent.api.workflow_task.CreateRequest", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskStatus]("kent.api.workflow_task.TaskStatus", "NodeIDs", "node_ids"),
		fieldRename[serverapi.WorkflowTaskDetail]("kent.api.workflow_task.TaskDetail", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskAssignedLabelIDs]("kent.api.workflow_task.AssignedLabelIds", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskLabelsUpdateRequest]("kent.api.workflow_task.LabelsUpdateRequest", "AddLabelIDs", "add_label_ids"),
		fieldRename[serverapi.WorkflowTaskLabelsUpdateRequest]("kent.api.workflow_task.LabelsUpdateRequest", "RemoveLabelIDs", "remove_label_ids"),
		fieldRename[serverapi.TaskSearchRequest]("kent.api.workflow_task.SearchRequest", "ProjectIDs", "project_ids"),
	}
}

func actualTargetScalarMappings() []WireScalarMapping {
	return []WireScalarMapping{
		scalarMapping("kent.api.shared.StreamCompletion", "code", protoreflect.Int32Kind),
		scalarMapping("kent.api.auth.SubscriptionWindowFacts", "duration_seconds", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ImportChoiceFact", "item_count", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ImportModeRecommendationFact", "item_count", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ModelFact", "context_window_tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ModelLargeWindowFact", "tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.onboarding.ContextWindowChoice", "tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.onboarding.FinalizeRequest", "model_timeout_seconds", protoreflect.Uint32Kind),
		scalarMapping("kent.api.process.InlineOutputRequest", "max_chars", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectDeleteBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectHomeListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectSummary", "session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectWorkspaceListRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectWorkspaceListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ListProjectWorkspacesSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ListProjectWorkspacesSuccess", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectWorkspaceSummary", "session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.WorkspaceUnlinkBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.prompt.QuestionAnswer", "selected_option_number", protoreflect.Int32Kind),
		scalarMapping("kent.api.connection.ServerIdentity", "pid", protoreflect.Int32Kind),
		scalarMapping("kent.api.session_launch.RunPromptOverrides", "model_timeout_seconds", protoreflect.Int32Kind),
		scalarMapping("kent.api.runtime.Status", "compaction_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.UnlinkProjectBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.WorkflowNodeGroup", "sort_order", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.AttentionListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardNodeCardsListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardNodeCardsListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardProject", "attached_workspace_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyAvailable", "remaining_capacity", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyDirectionProjection", "total_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyDirectionProjection", "unsatisfied_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyListDirection", "total_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyListDirection", "unsatisfied_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.ListRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.ListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchGroup", "total_hit_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchHit", "ordinal", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "context", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "blocker_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "directly_blocked_task_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "unsatisfied_blocker_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDetail", "attention_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDetail", "retained_session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskOffsetPageRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskOffsetPageRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.worktree.DirtyState", "dirty_file_count", protoreflect.Int32Kind),
	}
}

func actualTargetPresenceMappings() []WirePresenceMapping {
	var result []WirePresenceMapping
	result = append(result, presenceMappings[serverapi.ProcessListRequest]("kent.api.process.ListRequest", "owner_session_id", "owner_run_id")...)
	result = append(result, presenceMappings[serverapi.RunPromptOverrides]("kent.api.session_launch.RunPromptOverrides", "model", "provider_override", "thinking_level", "theme", "model_timeout_seconds", "tools", "openai_base_url")...)
	result = append(result, presenceMappings[protocol.StreamCompleteParams]("kent.api.shared.StreamCompletion", "code", "message", "transcript_close_reason")...)
	result = append(result, presenceMappings[protocol.ServerIdentity]("kent.api.connection.ServerIdentity", "persistence_root_id")...)
	result = append(result, presenceMappings[serverapi.AuthBootstrapOAuthConfig]("kent.api.auth.BootstrapOAuthConfig", "client_id", "issuer")...)
	result = append(result, presenceMappings[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "api_key", "callback_input", "redirect_uri", "oauth_state", "oauth_code_verifier", "device_authorization_code", "device_code_verifier")...)
	result = append(result, presenceMappings[serverapi.AuthCompleteBootstrapResponse]("kent.api.auth.BootstrapCompletion", "method_type", "account_id", "email")...)
	result = append(result, presenceMappingsAs[serverapi.AuthProviderSelection](false, "kent.api.auth.ProviderSelection", "provider_capabilities")...)
	result = append(result, presenceMappingsAs[serverapi.AuthStatusRequest](false, "kent.api.auth.GetStatusRequest", "provider")...)
	result = append(result, presenceMappingsAs[serverapi.AuthSubscriptionFacts](false, "kent.api.auth.SubscriptionFacts", "failure")...)
	result = append(result, presenceMappingsAs[serverapi.AuthSubscriptionWindowFacts](false, "kent.api.auth.SubscriptionWindowFacts", "reset_at")...)
	result = append(result, presenceMappingsAs[serverapi.CapabilityDefaultFacts](false, "kent.api.capability.DefaultFacts", "verbosity")...)
	result = append(result, presenceMappingsAs[serverapi.ImportRecommendationFacts](false, "kent.api.capability.ImportRecommendationFacts", "commands", "skills")...)
	result = append(result, presenceMappingsAs[serverapi.ModelCapabilityFact](false, "kent.api.capability.ModelFact", "large_window")...)
	result = append(result, presenceMappingsAs[serverapi.ProviderCapabilityFacts](false, "kent.api.capability.ProviderFacts", "current_effective")...)
	result = append(result, presenceMappings[serverapi.OnboardingContextWindowChoice]("kent.api.onboarding.ContextWindowChoice", "tokens")...)
	result = append(result, presenceMappingsAs[serverapi.OnboardingFinalizeRequest](false, "kent.api.onboarding.FinalizeRequest", "commands_import", "context_window", "main_provider", "model", "skills_import", "supervisor", "thinking")...)
	result = append(result, presenceMappings[serverapi.OnboardingModelChoice]("kent.api.onboarding.ModelChoice", "alias", "model_id")...)
	result = append(result, presenceMappingsAs[serverapi.OnboardingSupervisorChoice](false, "kent.api.onboarding.SupervisorChoice", "model", "thinking")...)
	result = append(result, presenceMappings[serverapi.OnboardingThinkingChoice]("kent.api.onboarding.ThinkingChoice", "level", "value")...)
	result = append(result, presenceMappings[serverapi.ProjectCreateRequest]("kent.api.project.CreateProjectRequest", "project_key")...)
	result = append(result, presenceMappings[serverapi.ProjectHomeListRequest]("kent.api.project.ProjectHomeListRequest", "page_size", "page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectUpdateRequest]("kent.api.project.UpdateProjectRequest", "project_key")...)
	result = append(result, presenceMappings[serverapi.ProjectWorkspaceListResponse]("kent.api.project.ListProjectWorkspacesSuccess", "next_offset")...)
	result = append(result, presenceMappings[serverapi.ProjectWorkspaceUnlinkBlocker]("kent.api.project.WorkspaceUnlinkBlocker", "count")...)
	result = append(result, presenceMappings[serverapi.RuntimeAppendCommittedEntryRequest]("kent.api.transcript.AppendCommittedEntryRequest", "notice_id", "visibility")...)
	result = append(result, presenceMappings[serverapi.RuntimeGoalSetRequest]("kent.api.runtime.GoalSetRequest", "run_id", "step_id")...)
	result = append(result, presenceMappings[serverapi.RuntimeGoalStatusRequest]("kent.api.runtime.GoalMutationRequest", "run_id", "step_id")...)
	result = append(result, presenceMappingsAs[serverapi.WorkflowNode](true, "kent.api.workflow_definition.WorkflowNode", "subagent_role")...)
	result = append(result, presenceMappingsAs[serverapi.WorkflowGraphDraftNode](true, "kent.api.workflow_definition.GraphDraftNode", "subagent_role")...)
	result = append(result, presenceMappings[clientui.SessionSummary]("kent.api.project.SessionSummary", "first_prompt_preview", "name")...)
	result = append(result, presenceMappings[serverapi.SessionInitialInputRequest]("kent.api.session_launch.SessionInitialInputRequest", "session_id")...)
	result = append(result, presenceMappings[serverapi.SessionPlan]("kent.api.session_launch.SessionPlan", "configured_model_name")...)
	result = append(result, presenceMappings[serverapi.SessionResolveTransitionRequest]("kent.api.session_launch.SessionResolveTransitionRequest", "session_id")...)
	result = append(result, presenceMappings[serverapi.SessionRuntimeReleaseRequest]("kent.api.session_launch.SessionRuntimeReleaseRequest", "close_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowAttentionListRequest]("kent.api.workflow_task.AttentionListRequest", "page_token")...)
	result = append(result, presenceMappings[serverapi.WorkflowContextSource]("kent.api.workflow_definition.ContextSource", "node_key")...)
	result = append(result, presenceMappings[serverapi.WorkflowCreateAndLinkProjectRequest]("kent.api.workflow_definition.CreateAndLinkProjectRequest", "default_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowGraphDraftNode]("kent.api.workflow_definition.GraphDraftNode", "completion_mode")...)
	result = append(result, presenceMappings[serverapi.WorkflowLinkProjectRequest]("kent.api.workflow_definition.LinkProjectRequest", "default_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowNode]("kent.api.workflow_definition.WorkflowNode", "completion_mode")...)
	result = append(result, presenceMappings[serverapi.WorkflowProjectSubscribeRequest]("kent.api.workflow_definition.ProjectSubscribeRequest", "project_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskCommentAddRequest]("kent.api.workflow_task.CommentAddRequest", "author_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskCreateRequest]("kent.api.workflow_task.CreateRequest", "body", "source_url", "source_workspace_id")...)
	result = append(result, presenceMappingsAs[serverapi.WorkflowTaskDependencyDirectionProjection](false, "kent.api.workflow_task.DependencyDirectionProjection", "add_availability")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskDetail]("kent.api.workflow_task.TaskDetail", "source_url")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskGetRequest]("kent.api.workflow_task.GetRequest", "project_id", "short_id", "task_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskInterruptRequest]("kent.api.workflow_task.InterruptRequest", "reason", "session_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskUpdateRequest]("kent.api.workflow_task.UpdateRequest", "source_workspace_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowUnlinkProjectRequest]("kent.api.workflow_definition.UnlinkProjectRequest", "replacement_default_link_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowValidateRequest]("kent.api.workflow_definition.ValidateRequest", "mode")...)
	result = append(result, presenceMappings[serverapi.WorktreeCreateRequest]("kent.api.worktree.CreateRequest", "base_ref", "branch_name", "root_path")...)
	result = append(result, presenceMappings[serverapi.WorktreeCreateTargetResolution]("kent.api.worktree.CreateTargetResolution", "resolved_ref")...)
	return result
}

// CheckExecutionTarget runs the complete migration-only inspection against the
// active legacy contract and the checked-in Protobuf descriptors.
func CheckExecutionTarget() error {
	report, err := InspectExecutionTarget()
	if err != nil {
		return err
	}
	classification, err := MergeDomainDeclarationSignoffs(ExecutionTargetDomainSignoffs())
	if err != nil {
		return err
	}
	operations, err := protoapi.Operations()
	if err != nil {
		return err
	}
	wireExceptions := actualTargetWireExceptions()
	coverage := BoundedMigrationCoverage{
		Report:                 report,
		Operations:             operations,
		Classification:         classification,
		WireExceptions:         wireExceptions,
		FieldRenames:           actualTargetFieldRenames(),
		ScalarMappings:         actualTargetScalarMappings(),
		PresenceMappings:       actualTargetPresenceMappings(),
		ExceptionalFingerprint: reviewedExceptionalWireFingerprint,
		FocusedFixtures: []FocusedProjectionFixture{
			{Name: FocusedKENT345StrictJSON, Check: checkKENT345StrictJSONFixture},
			{Name: FocusedKENT345CustomWire, Check: checkKENT345CustomWireFixture},
			{Name: FocusedKENT345Hydration, Check: checkKENT345HydrationFixture},
			{Name: FocusedKENT345Uniqueness, Check: checkKENT345UniquenessFixture},
			{Name: FocusedKENT345MixedValidators, Check: checkKENT345MixedValidatorsFixture},
			{Name: FocusedKENT554NegotiationValidation, Check: checkKENT554NegotiationValidationFixture},
			{Name: FocusedKENT554NegotiationConstants, Check: checkKENT554NegotiationConstantsFixture},
			{Name: FocusedKENT554RetainedCapabilityFacts, Check: checkKENT554RetainedCapabilityFactsFixture},
		},
	}
	return CheckBoundedMigrationCoverage(coverage)
}

func checkKENT345StrictJSONFixture() error {
	for _, message := range []proto.Message{
		&runpromptpb.Request{},
		&sessionlaunchpb.SessionPlanRequest{},
		&runtimepb.SubmitUserTurnRequest{},
	} {
		if message.ProtoReflect().Descriptor().Fields().ByName("client_request_id") != nil {
			return fmt.Errorf("%s retains client_request_id", message.ProtoReflect().Descriptor().FullName())
		}
		if err := protoapi.DecodeGeneratedMessage(nil, message); err == nil {
			return fmt.Errorf("%s accepted an invalid empty request", message.ProtoReflect().Descriptor().FullName())
		}
	}
	return nil
}

func checkKENT345CustomWireFixture() error {
	sessionID, err := runtimeids.ParseSessionID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		return err
	}
	queueItemID, err := runtimeids.ParseQueueItemID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		return err
	}
	message := &runtimepb.LiveSteerSuccess{
		QueueItemId: queueItemID.String(),
		Text:        "continue",
	}
	encoded, err := protoapi.EncodeGeneratedMessage(message)
	if err != nil {
		return err
	}
	var decoded runtimepb.LiveSteerSuccess
	if err := protoapi.DecodeGeneratedMessage(encoded, &decoded); err != nil {
		return err
	}
	if decoded.QueueItemId != queueItemID.String() {
		return fmt.Errorf("Queue Item identity round-trip = %q", decoded.QueueItemId)
	}
	request := &runtimepb.SubmitUserTurnRequest{SessionId: sessionID.String()}
	if request.ProtoReflect().Descriptor().Fields().ByName("session_id") == nil {
		return errors.New("retained Session identity is absent")
	}
	return nil
}

func checkKENT345HydrationFixture() error {
	first := "first"
	second := "second"
	for _, message := range []*transcriptpb.QueuedMessageState{
		{
			QueueItemId: "11111111-1111-4111-8111-111111111111",
			Status:      transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_ACCEPTED,
			Text:        &first,
		},
		{
			QueueItemId: "22222222-2222-4222-8222-222222222222",
			Status:      transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_ACCEPTED,
			Text:        &second,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err != nil {
			return err
		}
	}
	hydration := (&transcriptpb.Hydration{}).ProtoReflect().Descriptor()
	if hydration.Fields().ByName("queued_messages") == nil {
		return errors.New("generated hydration omits queued messages")
	}
	return nil
}

func checkKENT345UniquenessFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&transcriptpb.UserMessageFlushed{
		StepId: "44444444-4444-4444-8444-444444444444",
		QueueItemIds: []string{
			"11111111-1111-4111-8111-111111111111",
			"11111111-1111-4111-8111-111111111111",
		},
	}); err == nil {
		return errors.New("generated transcript event accepted duplicate Queue Item identity")
	}
	return nil
}

func checkKENT345MixedValidatorsFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&sessionlaunchpb.SessionPlanRequest{
		Mode: sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_HEADLESS,
	}); err == nil {
		return errors.New("generated Session plan accepted missing retained intent")
	}
	if err := protoapi.ValidateGeneratedMessage(&runtimepb.SubmitUserTurnRequest{
		SessionId: "session-1",
		Input: &runtimepb.UserTurnInput{
			Input: &runtimepb.UserTurnInput_Text{Text: "continue"},
		},
	}); err != nil {
		return err
	}
	if err := protoapi.ValidateGeneratedMessage(&runtimepb.SubmitUserTurnRequest{
		Input: &runtimepb.UserTurnInput{
			Input: &runtimepb.UserTurnInput_Text{Text: "continue"},
		},
	}); err == nil {
		return errors.New("mixed validator lost retained Session identity requirement")
	}
	return nil
}

func checkKENT554NegotiationValidationFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&connectionpb.HandshakeRequest{
		ProtocolVersion: protocol.Version,
	}); err != nil {
		return err
	}
	descriptor := (&connectionpb.HandshakeRequest{}).ProtoReflect().Descriptor()
	if descriptor.Fields().ByName("client_capabilities") != nil {
		return errors.New("generated handshake retains client capabilities")
	}
	legacy := protocol.HandshakeRequest{
		ProtocolVersion:    protocol.Version,
		ClientCapabilities: &protocol.ClientCapabilities{},
	}
	if err := legacy.Validate(); err == nil {
		return errors.New("legacy handshake no longer exercises capability negotiation")
	}
	return nil
}

func checkKENT554NegotiationConstantsFixture() error {
	if protocol.MethodCapabilityFactsGet != "capability.facts.get" {
		return fmt.Errorf("retained capability operation = %q", protocol.MethodCapabilityFactsGet)
	}
	return nil
}

func checkKENT554RetainedCapabilityFactsFixture() error {
	blank := " "
	if err := (serverapi.CapabilityFactsRequest{WorkspaceRoot: &blank}).Validate(); err == nil {
		return errors.New("retained capability facts accepted a blank workspace root")
	}
	generatedBlank := " "
	if err := protoapi.ValidateGeneratedMessage(&capabilitypb.GetFactsRequest{
		WorkspaceRoot:          &generatedBlank,
		ExplicitLlmProviderIds: []string{"openai", ""},
	}); err == nil {
		return errors.New("generated capability facts accepted an empty provider identity")
	}
	descriptor := (&capabilitypb.GetFactsRequest{}).ProtoReflect().Descriptor()
	if descriptor.Fields().ByName("explicit_llm_provider_ids") == nil {
		return errors.New("retained provider capability facts are absent")
	}
	if serverapi.ImportErrorItemKindSkill != "skill" {
		return fmt.Errorf("retained import capability constant = %q", serverapi.ImportErrorItemKindSkill)
	}
	return nil
}

func wireExceptionSignoff[T any](
	message protoreflect.FullName,
	classification WireExceptionClassification,
	legacyFingerprint string,
	descriptorFingerprint string,
) WireException {
	return WireException{
		LegacyType:            reflect.TypeFor[T](),
		Message:               message,
		Classification:        classification,
		LegacyFingerprint:     legacyFingerprint,
		DescriptorFingerprint: descriptorFingerprint,
	}
}

func fieldRename[T any](
	message protoreflect.FullName,
	legacyField string,
	descriptorField protoreflect.Name,
) WireFieldRename {
	return WireFieldRename{
		LegacyType:      reflect.TypeFor[T](),
		Message:         message,
		LegacyField:     legacyField,
		DescriptorField: descriptorField,
	}
}

func scalarMapping(
	message protoreflect.FullName,
	field protoreflect.Name,
	kind protoreflect.Kind,
) WireScalarMapping {
	return WireScalarMapping{Message: message, Field: field, Kind: kind}
}

func presenceMappings[T any](
	message protoreflect.FullName,
	fields ...protoreflect.Name,
) []WirePresenceMapping {
	return presenceMappingsAs[T](true, message, fields...)
}

func presenceMappingsAs[T any](
	optional bool,
	message protoreflect.FullName,
	fields ...protoreflect.Name,
) []WirePresenceMapping {
	result := make([]WirePresenceMapping, 0, len(fields))
	for _, field := range fields {
		result = append(result, WirePresenceMapping{
			LegacyType: reflect.TypeFor[T](),
			Message:    message,
			Field:      field,
			Optional:   optional,
		})
	}
	return result
}
