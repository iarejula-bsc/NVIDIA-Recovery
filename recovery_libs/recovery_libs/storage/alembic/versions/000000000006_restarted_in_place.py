# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Record which attempts were restarted inside their allocation.

Revision ID: 000000000006
Revises: 000000000005
Create Date: 2026-10-27
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

revision: str = "000000000006"
down_revision: str | None = "000000000005"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    """Add the column; existing attempts were never restarted inside their allocation.

    restarted_in_place is not nullable, so existing rows are filled with false
    through a temporary server default that is then dropped to match the model.
    """
    op.add_column(
        "job_execution_records",
        sa.Column("restarted_in_place", sa.Boolean(), nullable=False, server_default=sa.false()),
    )
    op.alter_column("job_execution_records", "restarted_in_place", existing_type=sa.Boolean(), server_default=None)


def downgrade() -> None:
    """Drop the column; recorded restarts inside the allocation are discarded."""
    op.drop_column("job_execution_records", "restarted_in_place")
