Implement chess perft (performance test) in Go in the current working directory.

Background: perft(N) counts the number of legal chess move sequences of length N from a given position. It exercises the move generator's correctness.

API:
- `Perft(fen string, depth int) uint64` — given a FEN string and a depth, return the perft node count

Required: legal-move generation including
- pawn moves (single push, double push from start rank, captures, en-passant, promotion)
- knight moves
- bishop / rook / queen sliding moves
- king moves (including castling, both kingside and queenside)
- check detection (a move is legal only if the king is not in check after it)

Reference perft values (must match exactly):

Initial position `rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1`:
- perft(1) = 20
- perft(2) = 400
- perft(3) = 8902

Kiwipete position `r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1`:
- perft(1) = 48
- perft(2) = 2039
- perft(3) = 97862

Include `go test ./...` that verifies BOTH positions at perft(1), perft(2), AND perft(3). All 6 values must match exactly.

Initialize go.mod yourself. Use any board representation you like (mailbox, bitboard, 0x88, etc.).

Done when `go test ./...` passes with all 6 perft values matching.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
