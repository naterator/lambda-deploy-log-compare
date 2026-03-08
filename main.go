package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

func main() {
	captureCmd := flag.NewFlagSet("capture", flag.ContinueOnError)
	captureCmd.Usage = func() { printUsage() }
	captureFunc := captureCmd.String("function", "", "Lambda function name(s), comma-separated")
	captureCount := captureCmd.Int("count", 20, "Number of invocations to capture")
	captureOffset := captureCmd.Int("offset", 0, "Skip this many recent invocations before capturing (e.g., 100 to go back 100 runs)")
	captureLabel := captureCmd.String("label", "", "Label for this snapshot (e.g., 'pre-deploy', 'post-deploy-staging')")
	captureOutDir := captureCmd.String("out", ".", "Output directory for snapshot files")
	captureRegion := captureCmd.String("region", "us-west-2", "AWS region")
	captureProfile := captureCmd.String("profile", "", "AWS CLI profile (optional)")

	compareCmd := flag.NewFlagSet("compare", flag.ContinueOnError)
	compareCmd.Usage = func() { printUsage() }
	compareFileA := compareCmd.String("a", "", "Path to first snapshot file (baseline)")
	compareFileB := compareCmd.String("b", "", "Path to second snapshot file (new deployment)")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "capture":
		if err := captureCmd.Parse(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		functions := parseFunctionNames(*captureFunc)
		if err := validateCaptureInputs(functions, *captureLabel, *captureCount, *captureOffset); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			printUsage()
			os.Exit(1)
		}

		client, err := newLogsClient(*captureRegion, *captureProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing AWS client: %v\n", err)
			os.Exit(1)
		}

		var captureErrors int
		for _, fn := range functions {
			logGroup := logGroupForFunction(fn)
			err := runCapture(client, fn, logGroup, *captureCount, *captureOffset, *captureLabel, *captureOutDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error capturing %s: %v\n", fn, err)
				captureErrors++
			}
		}
		if captureErrors > 0 {
			fmt.Fprintf(os.Stderr, "%d capture(s) failed\n", captureErrors)
			os.Exit(1)
		}

	case "compare":
		if err := compareCmd.Parse(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		if *compareFileA == "" || *compareFileB == "" {
			fmt.Fprintln(os.Stderr, "Error: --a and --b are required")
			printUsage()
			os.Exit(1)
		}
		if err := runCompare(*compareFileA, *compareFileB); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `lambda-deploy-log-compare - Compare Lambda invocation logs across deployments

Usage:
  lambda-deploy-log-compare capture --function <name>[,<name>,...] --label <label> [options]
  lambda-deploy-log-compare compare --a <baseline.json> --b <new.json>

Commands:
  capture   Capture the most recent invocation logs for one or more Lambda functions
  compare   Compare two snapshots side-by-side

Capture options:
  --function   Lambda function name(s), comma-separated (required)
  --label      Label for this snapshot, e.g. "pre-deploy" (required)
  --count      Number of invocations to capture; must be > 0 (default: 20)
  --offset     Skip this many recent invocations before capturing; must be >= 0 (default: 0)
  --out        Output directory for snapshot files (default: current dir)
  --region     AWS region (default: us-west-2)
  --profile    AWS CLI profile name (optional)

Compare options:
  --a          Path to baseline snapshot JSON file (required)
  --b          Path to new deployment snapshot JSON file (required)

Log groups are derived as /aws/lambda/<function-name>.

Typical workflow:
  1. Before deploying: capture baseline snapshots
     $ lambda-deploy-log-compare capture --function my_func_a,my_func_b --label pre-deploy --out ./snapshots

  2. Deploy your code to the environment

  3. Wait for some invocations, then capture again
     $ lambda-deploy-log-compare capture --function my_func_a,my_func_b --label post-deploy --out ./snapshots

  4. Compare the snapshots
     $ lambda-deploy-log-compare compare \
         --a ./snapshots/my_func_a_pre-deploy.json \
         --b ./snapshots/my_func_a_post-deploy.json
`)
}

func parseFunctionNames(raw string) []string {
	var names []string
	for _, fn := range strings.Split(raw, ",") {
		fn = strings.TrimSpace(fn)
		if fn != "" {
			names = append(names, fn)
		}
	}
	return names
}

func validateCaptureInputs(functions []string, label string, count, offset int) error {
	switch {
	case label == "":
		return fmt.Errorf("--label is required")
	case len(functions) == 0:
		return fmt.Errorf("--function is required")
	case count <= 0:
		return fmt.Errorf("--count must be greater than 0")
	case offset < 0:
		return fmt.Errorf("--offset must be greater than or equal to 0")
	default:
		return nil
	}
}

func newLogsClient(region, profile string) (*cloudwatchlogs.Client, error) {
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(region))
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	return cloudwatchlogs.NewFromConfig(cfg), nil
}
