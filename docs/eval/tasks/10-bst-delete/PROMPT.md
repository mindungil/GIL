Implement a binary search tree (BST) with **delete** in Go in the current working directory.

API:
- `New() *BST` — create empty BST holding int keys
- `(t *BST) Insert(key int)` — insert; duplicates are no-op
- `(t *BST) Search(key int) bool` — return true if key exists
- `(t *BST) Delete(key int)` — remove key; no-op if not present
- `(t *BST) InOrder() []int` — return all keys in ascending order
- `(t *BST) Len() int` — number of keys

Delete semantics (the standard textbook three-case):
1. Leaf: remove it
2. One child: replace node with its child
3. Two children: replace node's value with its **in-order successor** (smallest key in right subtree), then delete that successor

The tree does NOT need to self-balance (plain BST, not red-black or AVL).

Include `go test ./...` with at minimum:
1. Insert + Search basic
2. InOrder returns sorted (insert in shuffled order, verify sorted output)
3. Delete leaf
4. Delete node with one child
5. Delete node with two children (verifying in-order property still holds)
6. Delete root with two children (specific case)
7. Delete non-existent key is no-op
8. **Stress test**: insert 200 random ints, then delete a random 100 of them, verify `InOrder()` returns exactly the surviving keys in sorted order. Repeat for 50 seeds.

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
