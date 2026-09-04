package protoapi

import (
	"fmt"
	"math"
	"strings"
	"time"

	"core/shared/clientui"
	processpb "core/shared/protoapi/gen/kent/api/process"
	"core/shared/serverapi"
	"core/shared/textutil"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func ProcessListRequestToProto(request serverapi.ProcessListRequest) *processpb.ListRequest {
	return &processpb.ListRequest{
		ProjectId:      strings.TrimSpace(request.ProjectID),
		OwnerSessionId: normalizedOptionalString(request.OwnerSessionID),
		OwnerRunId:     normalizedOptionalString(request.OwnerRunID),
	}
}

func ProcessListRequestFromProto(request *processpb.ListRequest) serverapi.ProcessListRequest {
	return serverapi.ProcessListRequest{
		ProjectID:      strings.TrimSpace(request.GetProjectId()),
		OwnerSessionID: normalizedOptionalString(request.OwnerSessionId),
		OwnerRunID:     normalizedOptionalString(request.OwnerRunId),
	}
}

func ProcessKillRequestToProto(request serverapi.ProcessKillRequest) *processpb.KillRequest {
	return &processpb.KillRequest{ProcessId: strings.TrimSpace(request.ProcessID)}
}

func ProcessKillRequestFromProto(request *processpb.KillRequest) serverapi.ProcessKillRequest {
	return serverapi.ProcessKillRequest{ProcessID: strings.TrimSpace(request.GetProcessId())}
}

func ProcessListResponseFromProto(success *processpb.ListSuccess) (serverapi.ProcessListResponse, error) {
	processes := make([]clientui.BackgroundProcess, 0, len(success.GetProcesses()))
	for _, process := range success.GetProcesses() {
		converted, err := BackgroundProcessFromProto(process)
		if err != nil {
			return serverapi.ProcessListResponse{}, err
		}
		processes = append(processes, converted)
	}
	return serverapi.ProcessListResponse{Processes: processes}, nil
}

func ProcessListSuccessToProto(response serverapi.ProcessListResponse) (*processpb.ListSuccess, error) {
	processes := make([]*processpb.BackgroundProcess, 0, len(response.Processes))
	for _, process := range response.Processes {
		converted, err := BackgroundProcessToProto(process)
		if err != nil {
			return nil, err
		}
		processes = append(processes, converted)
	}
	return &processpb.ListSuccess{Processes: processes}, nil
}

func BackgroundProcessToProto(process clientui.BackgroundProcess) (*processpb.BackgroundProcess, error) {
	exitCode, err := processExitCodeToProto(process.ExitCode)
	if err != nil {
		return nil, fmt.Errorf("process %q: %w", process.ID, err)
	}
	message := &processpb.BackgroundProcess{
		Id:                      process.ID,
		OwnerSessionId:          process.OwnerSessionID,
		OwnerRunId:              process.OwnerRunID,
		OwnerStepId:             process.OwnerStepID,
		State:                   process.State,
		Command:                 process.Command,
		Workdir:                 process.Workdir,
		StartedAt:               timestamppb.New(process.StartedAt),
		ExitCode:                exitCode,
		LogPath:                 process.LogPath,
		OutputAvailable:         process.OutputAvailable,
		OutputRetainedFromBytes: process.OutputRetainedFromBytes,
		OutputRetainedToBytes:   process.OutputRetainedToBytes,
		Running:                 process.Running,
		StdinOpen:               process.StdinOpen,
		Backgrounded:            process.Backgrounded,
		KillRequested:           process.KillRequested,
		LastUpdatedAt:           timestamppb.New(process.LastUpdatedAt),
		RecentOutput:            process.RecentOutput,
	}
	if !process.FinishedAt.IsZero() {
		message.FinishedAt = timestamppb.New(process.FinishedAt)
	}
	return message, nil
}

func BackgroundProcessFromProto(process *processpb.BackgroundProcess) (clientui.BackgroundProcess, error) {
	startedAt, err := requiredProcessTime(process.GetStartedAt(), "started_at")
	if err != nil {
		return clientui.BackgroundProcess{}, fmt.Errorf("process %q: %w", process.GetId(), err)
	}
	lastUpdatedAt, err := requiredProcessTime(process.GetLastUpdatedAt(), "last_updated_at")
	if err != nil {
		return clientui.BackgroundProcess{}, fmt.Errorf("process %q: %w", process.GetId(), err)
	}
	finishedAt, err := optionalProcessTime(process.GetFinishedAt(), "finished_at")
	if err != nil {
		return clientui.BackgroundProcess{}, fmt.Errorf("process %q: %w", process.GetId(), err)
	}
	var exitCode *int
	if process.ExitCode != nil {
		value := int(*process.ExitCode)
		exitCode = &value
	}
	return clientui.BackgroundProcess{
		ID:                      process.GetId(),
		OwnerSessionID:          process.GetOwnerSessionId(),
		OwnerRunID:              process.GetOwnerRunId(),
		OwnerStepID:             process.GetOwnerStepId(),
		State:                   process.GetState(),
		Command:                 process.GetCommand(),
		Workdir:                 process.GetWorkdir(),
		StartedAt:               startedAt,
		FinishedAt:              finishedAt,
		ExitCode:                exitCode,
		LogPath:                 process.GetLogPath(),
		RecentOutput:            process.GetRecentOutput(),
		OutputAvailable:         process.GetOutputAvailable(),
		OutputRetainedFromBytes: process.GetOutputRetainedFromBytes(),
		OutputRetainedToBytes:   process.GetOutputRetainedToBytes(),
		Running:                 process.GetRunning(),
		StdinOpen:               process.GetStdinOpen(),
		Backgrounded:            process.GetBackgrounded(),
		KillRequested:           process.GetKillRequested(),
		LastUpdatedAt:           lastUpdatedAt,
	}, nil
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	return textutil.OptionalTrimmedString(*value)
}

func processExitCodeToProto(exitCode *int) (*int32, error) {
	if exitCode == nil {
		return nil, nil
	}
	if *exitCode < math.MinInt32 || *exitCode > math.MaxInt32 {
		return nil, fmt.Errorf("exit code %d is outside int32 range", *exitCode)
	}
	value := int32(*exitCode)
	return &value, nil
}

func requiredProcessTime(timestamp *timestamppb.Timestamp, field string) (time.Time, error) {
	if timestamp == nil {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	if err := timestamp.CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("%s is invalid: %w", field, err)
	}
	return timestamp.AsTime(), nil
}

func optionalProcessTime(timestamp *timestamppb.Timestamp, field string) (time.Time, error) {
	if timestamp == nil {
		return time.Time{}, nil
	}
	if err := timestamp.CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("%s is invalid: %w", field, err)
	}
	return timestamp.AsTime(), nil
}
