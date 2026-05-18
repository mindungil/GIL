In this directory there is a working Go module with `shipping.go` containing two functions: `ShippingCost` and `Discount`. Both have a similar tier-based switch (`gold` / `silver` / `bronze` / default) and the rules duplicate the same shape.

Your task: **refactor** the tier handling to remove duplication. Create a new file `tiers.go` with a single helper function — design the helper yourself such that BOTH `ShippingCost` and `Discount` can use it. Replace the inline switches in both call sites with calls to your new helper.

Constraints:
1. All existing tests in `shipping_test.go` must still pass.
2. Do NOT modify `shipping_test.go`.
3. The behavior of `ShippingCost` and `Discount` must be byte-identical before and after — this is a behavior-preserving refactor.
4. The new helper must live in `tiers.go` (a new file).

Run `go test ./...` to verify the starting state is green, then apply your refactor, then re-verify.

Done when `go test ./...` passes and `tiers.go` exists with a function used by both `ShippingCost` and `Discount`.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
