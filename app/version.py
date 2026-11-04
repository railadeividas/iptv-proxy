"""Application version, set at image build time (--build-arg VERSION=...)."""

import os

VERSION = os.getenv("APP_VERSION") or "dev"
