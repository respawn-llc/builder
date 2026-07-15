package app

import (
	"fmt"
	"time"
)

type sessionPickerRelativeAgeBucket string

const (
	sessionPickerRelativeAgeJustNow sessionPickerRelativeAgeBucket = "just_now"
	sessionPickerRelativeAgeMinutes sessionPickerRelativeAgeBucket = "minutes"
	sessionPickerRelativeAgeHours   sessionPickerRelativeAgeBucket = "hours"
	sessionPickerRelativeAgeDays    sessionPickerRelativeAgeBucket = "days"
	sessionPickerRelativeAgeWeeks   sessionPickerRelativeAgeBucket = "weeks"
	sessionPickerRelativeAgeMonths  sessionPickerRelativeAgeBucket = "months"
	sessionPickerRelativeAgeFuture  sessionPickerRelativeAgeBucket = "future"
)

type sessionPickerRelativeAge struct {
	Bucket sessionPickerRelativeAgeBucket
	Amount int
}

func (a sessionPickerRelativeAge) String() string {
	switch a.Bucket {
	case sessionPickerRelativeAgeFuture:
		return "in the future"
	case sessionPickerRelativeAgeJustNow:
		return "just now"
	case sessionPickerRelativeAgeMinutes:
		return fmt.Sprintf("%dm ago", a.Amount)
	case sessionPickerRelativeAgeHours:
		return fmt.Sprintf("%dh ago", a.Amount)
	case sessionPickerRelativeAgeDays:
		return fmt.Sprintf("%dd ago", a.Amount)
	case sessionPickerRelativeAgeWeeks:
		return fmt.Sprintf("%dw ago", a.Amount)
	case sessionPickerRelativeAgeMonths:
		return fmt.Sprintf("%dmo ago", a.Amount)
	default:
		return "—"
	}
}

func relativeSessionAge(updatedAt, now time.Time) sessionPickerRelativeAge {
	age := now.Sub(updatedAt)
	if age < 0 {
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeFuture}
	}
	switch {
	case age < time.Minute:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeJustNow}
	case age < time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeMinutes, Amount: int(age / time.Minute)}
	case age < 24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeHours, Amount: int(age / time.Hour)}
	case age < 7*24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeDays, Amount: int(age / (24 * time.Hour))}
	case age < 30*24*time.Hour:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeWeeks, Amount: int(age / (7 * 24 * time.Hour))}
	default:
		return sessionPickerRelativeAge{Bucket: sessionPickerRelativeAgeMonths, Amount: int(age / (30 * 24 * time.Hour))}
	}
}
