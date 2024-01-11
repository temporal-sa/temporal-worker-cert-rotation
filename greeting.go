package app

import (
	"go.temporal.io/sdk/workflow"
)

func GreetSomeone(ctx workflow.Context, name string) (string, error) {
	workflow.GetLogger(ctx).Info("GreetSomeone workflow started.", "name", name)
	return "Hello " + name + "!", nil
}
