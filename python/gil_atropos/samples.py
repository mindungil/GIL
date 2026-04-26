"""Bundled fallback dataset of tiny coding tasks.

These exist so ``GilCodingEnv`` can run without HuggingFace ``datasets``
installed. Each task ships with a deterministic verifier (a shell command,
typically a one-line ``pytest``) that gild's run loop will execute as the
``Verification.checks`` block on the frozen spec.

Each task is portable: the verifier writes a tiny pytest file in the
working dir before calling pytest, so tasks don't depend on any pre-existing
fixture files.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class CodingTask:
    """One bundled coding task.

    Attributes
    ----------
    task_id:
        Stable identifier (used in eval logs / wandb tables).
    prompt:
        Natural-language description sent to the agent as the goal one-liner.
    detailed:
        Longer description for ``Goal.detailed`` (optional).
    target_file:
        File the agent is expected to create / modify.
    verifier_cmd:
        Shell command run after the agent finishes. Exit 0 == pass.
    success_criteria:
        Human-readable bullets surfaced to the agent in the spec.
    """

    task_id: str
    prompt: str
    detailed: str
    target_file: str
    verifier_cmd: str
    success_criteria: list[str] = field(default_factory=list)

    def to_spec_dict(self, *, working_dir: str = "") -> dict[str, Any]:
        """Render this task as the spec_dict expected by ``freeze_spec``."""
        return {
            "goal": {
                "one_liner": self.prompt,
                "detailed": self.detailed,
                "success_criteria_natural": list(self.success_criteria),
                "non_goals": [],
            },
            "constraints": {
                "tech_stack": ["python>=3.10"],
                "forbidden": ["network access"],
            },
            "verification": {
                "checks": [
                    {
                        "name": f"{self.task_id}_verify",
                        "kind": "SHELL",
                        "command": self.verifier_cmd,
                        "expected_exit_code": 0,
                    }
                ],
                "max_retries_per_check": 1,
                "per_check_timeout_seconds": 60,
            },
            "workspace": {"backend": "LOCAL_NATIVE", "path": working_dir},
            "budget": {
                "max_total_tokens": 50_000,
                "max_total_cost_usd": 1.0,
                "max_wall_clock_seconds": 600,
                "max_iterations": 30,
                "max_subagent_depth": 1,
            },
            "tools": {"bash": True, "file_ops": True},
            "risk": {"autonomy": "FULL"},
            # Hint surfaced to the agent (gil consults this; not part of spec proto).
            "_target_file_hint": self.target_file,
        }


# --- The five bundled tasks -------------------------------------------------
#
# Each ``verifier_cmd`` writes its own pytest file (using a heredoc) to keep
# the task self-contained. We exec pytest in quiet mode so the run output
# stays small.

_PYTEST_PRELUDE = "set -e; cd \"$(pwd)\"; "


BUNDLED_TASKS: list[CodingTask] = [
    CodingTask(
        task_id="fibonacci",
        prompt="Write a Python function `fib(n)` that returns the n-th Fibonacci number.",
        detailed=(
            "Create a file `solution.py` exposing `fib(n: int) -> int`. "
            "fib(0) == 0, fib(1) == 1, fib(n) == fib(n-1) + fib(n-2). "
            "It must handle n up to 30 in well under a second."
        ),
        target_file="solution.py",
        verifier_cmd=(
            _PYTEST_PRELUDE
            + "cat > _test_fib.py <<'PY'\n"
            + "from solution import fib\n"
            + "def test_base(): assert fib(0) == 0 and fib(1) == 1\n"
            + "def test_seq(): assert [fib(i) for i in range(8)] == [0,1,1,2,3,5,8,13]\n"
            + "def test_n10(): assert fib(10) == 55\n"
            + "PY\n"
            + "python -m pytest _test_fib.py -q"
        ),
        success_criteria=[
            "solution.py exists at the workspace root.",
            "`fib(10) == 55`.",
            "All pytest cases pass.",
        ],
    ),
    CodingTask(
        task_id="reverse_string",
        prompt="Write a Python function `reverse(s)` that returns the reverse of a string.",
        detailed=(
            "Create `solution.py` with `reverse(s: str) -> str`. "
            "Empty string and unicode must work."
        ),
        target_file="solution.py",
        verifier_cmd=(
            _PYTEST_PRELUDE
            + "cat > _test_rev.py <<'PY'\n"
            + "from solution import reverse\n"
            + "def test_empty(): assert reverse('') == ''\n"
            + "def test_ascii(): assert reverse('hello') == 'olleh'\n"
            + "def test_unicode(): assert reverse('한글') == '글한'\n"
            + "PY\n"
            + "python -m pytest _test_rev.py -q"
        ),
        success_criteria=[
            "solution.py exists.",
            "reverse handles empty string and unicode.",
        ],
    ),
    CodingTask(
        task_id="is_palindrome",
        prompt="Write a Python function `is_palindrome(s)` that returns True iff s reads the same forwards and backwards (case-insensitive, ignoring non-alphanumerics).",
        detailed=(
            "Create `solution.py` with `is_palindrome(s: str) -> bool`. "
            "'A man, a plan, a canal: Panama' is a palindrome."
        ),
        target_file="solution.py",
        verifier_cmd=(
            _PYTEST_PRELUDE
            + "cat > _test_pal.py <<'PY'\n"
            + "from solution import is_palindrome\n"
            + "def test_simple(): assert is_palindrome('racecar') is True\n"
            + "def test_neg(): assert is_palindrome('hello') is False\n"
            + "def test_punct(): assert is_palindrome('A man, a plan, a canal: Panama') is True\n"
            + "PY\n"
            + "python -m pytest _test_pal.py -q"
        ),
        success_criteria=["solution.py exists; ignores case + non-alnum."],
    ),
    CodingTask(
        task_id="fizzbuzz",
        prompt="Write a Python function `fizzbuzz(n)` returning a list of length n with FizzBuzz strings.",
        detailed=(
            "Create `solution.py` with `fizzbuzz(n: int) -> list[str]`. "
            "Index i (1-based): 'Fizz' if i%3==0, 'Buzz' if i%5==0, "
            "'FizzBuzz' if both, otherwise str(i)."
        ),
        target_file="solution.py",
        verifier_cmd=(
            _PYTEST_PRELUDE
            + "cat > _test_fb.py <<'PY'\n"
            + "from solution import fizzbuzz\n"
            + "def test_15():\n"
            + "    assert fizzbuzz(15) == ['1','2','Fizz','4','Buzz','Fizz','7','8','Fizz','Buzz','11','Fizz','13','14','FizzBuzz']\n"
            + "PY\n"
            + "python -m pytest _test_fb.py -q"
        ),
        success_criteria=["fizzbuzz(15) matches the canonical sequence."],
    ),
    CodingTask(
        task_id="sum_csv_column",
        prompt="Write a Python script `solution.py` exposing `sum_column(path, col_name)` that reads a CSV and sums the named numeric column.",
        detailed=(
            "Use the stdlib `csv` module. Skip rows where the value isn't a "
            "number. Return a float."
        ),
        target_file="solution.py",
        verifier_cmd=(
            _PYTEST_PRELUDE
            + "cat > _data.csv <<'CSV'\n"
            + "name,score\n"
            + "alice,10\n"
            + "bob,20.5\n"
            + "carol,bad\n"
            + "dave,12\n"
            + "CSV\n"
            + "cat > _test_csv.py <<'PY'\n"
            + "from solution import sum_column\n"
            + "def test_sum(): assert abs(sum_column('_data.csv', 'score') - 42.5) < 1e-9\n"
            + "PY\n"
            + "python -m pytest _test_csv.py -q"
        ),
        success_criteria=[
            "sum_column ignores non-numeric rows.",
            "Returns a float (not int).",
        ],
    ),
]


def get_task(task_id: str) -> CodingTask:
    """Lookup helper for ad-hoc eval / tests."""
    for t in BUNDLED_TASKS:
        if t.task_id == task_id:
            return t
    raise KeyError(task_id)
