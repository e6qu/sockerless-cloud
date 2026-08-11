package aws_cli_test

import (
	"encoding/json"
	"testing"
)

func TestBudgetsCLI_CRUD(t *testing.T) {
	const accountID = "123456789012"
	const budgetName = "cli-budget-crud"

	budget := map[string]any{
		"BudgetName": budgetName,
		"BudgetLimit": map[string]string{
			"Amount": "100",
			"Unit":   "USD",
		},
		"TimeUnit":   "MONTHLY",
		"BudgetType": "COST",
	}
	budgetJSON, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}

	runCLI(t, awsCLI(
		"budgets", "create-budget",
		"--account-id", accountID,
		"--budget", string(budgetJSON),
		"--output", "json",
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI(
			"budgets", "delete-budget",
			"--account-id", accountID,
			"--budget-name", budgetName,
		))
	})

	out := runCLI(t, awsCLI(
		"budgets", "describe-budget",
		"--account-id", accountID,
		"--budget-name", budgetName,
		"--output", "json",
	))
	var described struct {
		Budget struct {
			BudgetName  string `json:"BudgetName"`
			BudgetLimit struct {
				Amount string `json:"Amount"`
				Unit   string `json:"Unit"`
			} `json:"BudgetLimit"`
		} `json:"Budget"`
	}
	parseJSON(t, out, &described)
	if described.Budget.BudgetName != budgetName ||
		described.Budget.BudgetLimit.Amount != "100" ||
		described.Budget.BudgetLimit.Unit != "USD" {
		t.Fatalf("describe-budget returned unexpected budget: %s", out)
	}

	budget["BudgetLimit"] = map[string]string{"Amount": "250", "Unit": "USD"}
	updatedJSON, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}
	runCLI(t, awsCLI(
		"budgets", "update-budget",
		"--account-id", accountID,
		"--new-budget", string(updatedJSON),
		"--output", "json",
	))

	listed := runCLI(t, awsCLI(
		"budgets", "describe-budgets",
		"--account-id", accountID,
		"--output", "json",
	))
	var budgets struct {
		Budgets []struct {
			BudgetName  string `json:"BudgetName"`
			BudgetLimit struct {
				Amount string `json:"Amount"`
			} `json:"BudgetLimit"`
		} `json:"Budgets"`
	}
	parseJSON(t, listed, &budgets)
	for _, item := range budgets.Budgets {
		if item.BudgetName == budgetName && item.BudgetLimit.Amount == "250" {
			runCLI(t, awsCLI(
				"budgets", "delete-budget",
				"--account-id", accountID,
				"--budget-name", budgetName,
			))
			return
		}
	}
	t.Fatalf("describe-budgets did not return the updated budget: %s", listed)
}
