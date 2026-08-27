package series

import (
	"encoding/json"
	"fmt"
	"io"
)

func WriteJSON(w io.Writer, admission Admission) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(admission)
}

func WriteSummary(w io.Writer, admission Admission) {
	fmt.Fprintln(w, "=== 系列性能证据准入 ===")
	fmt.Fprintf(w, "校准报告: %v\n", admission.CalibrationFiles)
	fmt.Fprintf(w, "正式报告: %s\n", admission.MeasurementFile)
	fmt.Fprintf(w, "允许声称性能收益: %t\n", admission.EligibleForPerformanceClaim)
	fmt.Fprintf(w, "证据等级: %s\n", admission.EvidenceLevel)
	if len(admission.RejectionReasons) == 0 {
		fmt.Fprintln(w, "拒绝原因: 无")
		return
	}
	for _, reason := range admission.RejectionReasons {
		fmt.Fprintf(w, "拒绝原因: %s (%s)\n", reason.Code, reason.Source)
	}
}
