package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/report"
	"sqlsentinel/internal/series"
)

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func runAdmitSeries(args []string) error {
	fs := flag.NewFlagSet("admit-series", flag.ExitOnError)
	var calibrationPaths stringList
	fs.Var(&calibrationPaths, "calibration", "只标定 bench JSON；至少传入两次")
	measurementPath := fs.String("measurement", "", "正式测量 bench JSON")
	outPath := fs.String("out", "series_admission.json", "系列准入 JSON 输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(calibrationPaths) < 2 || *measurementPath == "" {
		return fmt.Errorf("至少需要两次 --calibration 和一次 --measurement")
	}

	calibrations := make([]series.Input, 0, len(calibrationPaths))
	for _, path := range calibrationPaths {
		r, err := readBenchReport(path)
		if err != nil {
			return err
		}
		calibrations = append(calibrations, series.Input{Source: path, Report: r})
	}
	measurement, err := readBenchReport(*measurementPath)
	if err != nil {
		return err
	}

	admission, err := series.Evaluate(calibrations, series.Input{Source: *measurementPath, Report: measurement})
	if err != nil {
		return err
	}
	f, err := os.Create(*outPath)
	if err != nil {
		return fmt.Errorf("创建系列准入报告失败: %w", err)
	}
	if err := series.WriteJSON(f, admission); err != nil {
		f.Close()
		return fmt.Errorf("写入系列准入报告失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	series.WriteSummary(os.Stdout, admission)
	fmt.Printf("JSON 报告: %s\n", *outPath)
	return nil
}

func readBenchReport(path string) (report.Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return report.Result{}, fmt.Errorf("读取报告 %q 失败: %w", path, err)
	}
	defer f.Close()
	var result report.Result
	if err := json.NewDecoder(f).Decode(&result); err != nil {
		return report.Result{}, fmt.Errorf("解析报告 %q 失败: %w", path, err)
	}
	return result, nil
}
