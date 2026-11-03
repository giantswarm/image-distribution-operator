package clouddirector

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyTemplateStatus(t *testing.T) {
	testCases := []struct {
		name     string
		status   int
		expected templateHealth
	}{
		{name: "FAILED_CREATION is broken", status: -1, expected: templateBroken},
		{name: "UNRESOLVED is broken", status: 0, expected: templateBroken},
		{name: "RESOLVED is healthy", status: 1, expected: templateHealthy},
		{name: "POWERED_OFF is healthy", status: 8, expected: templateHealthy},
		{name: "DESCRIPTOR_PENDING is broken", status: 11, expected: templateBroken},
		{name: "DISK_CONTENTS_PENDING is broken", status: 13, expected: templateBroken},
		{name: "TRANSFER_TIMEOUT is broken", status: 17, expected: templateBroken},
		{name: "COPYING_CONTENTS is unexpected", status: 12, expected: templateUnexpected},
		{name: "QUARANTINED is unexpected", status: 14, expected: templateUnexpected},
		{name: "POWERED_ON is unexpected", status: 4, expected: templateUnexpected},
		{name: "unknown status code is unexpected", status: 42, expected: templateUnexpected},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, classifyTemplateStatus(tc.status))
		})
	}
}
