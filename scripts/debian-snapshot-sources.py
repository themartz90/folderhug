#!/usr/bin/env python3
"""Recover signed source repositories from a Debian image's snapshot records."""

import datetime
import pathlib
import re
import sys


def snapshot_sources(content: str) -> str:
    entries = []
    for stanza in re.split(r"\n\s*\n", content.strip()):
        snapshots = re.findall(
            r"^# https?://snapshot\.debian\.org/archive/(debian(?:-security)?)/(\d{8}T\d{6}Z)\s*$",
            stanza,
            re.MULTILINE,
        )
        if not snapshots:
            continue
        if len(snapshots) != 1:
            raise ValueError("Expected one Debian snapshot per source stanza")
        archive, timestamp = snapshots[0]
        datetime.datetime.strptime(timestamp, "%Y%m%dT%H%M%SZ")
        fields = {}
        for line in stanza.splitlines():
            if not line or line.startswith("#"):
                continue
            name, separator, value = line.partition(":")
            if not separator or name in fields:
                raise ValueError("Invalid Debian source stanza")
            fields[name] = value.strip()
        if fields.get("Signed-By") != "/usr/share/keyrings/debian-archive-keyring.gpg":
            raise ValueError("Expected Debian archive signing keyring")
        if fields.get("URIs") not in (
            f"http://deb.debian.org/{archive}",
            f"https://deb.debian.org/{archive}",
        ):
            raise ValueError("Expected an official Debian repository")
        suites = fields.get("Suites", "")
        components = fields.get("Components", "")
        if not all(re.fullmatch(r"[a-z0-9-]+(?: [a-z0-9-]+)*", value) for value in (suites, components)):
            raise ValueError("Invalid Debian suites or components")
        entries.append(
            f"Types: deb-src\nURIs: https://snapshot.debian.org/archive/{archive}/{timestamp}/\n"
            f"Suites: {suites}\nComponents: {components}\n"
            "Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg\n"
            "Check-Valid-Until: no\n"
        )
    if not entries:
        raise ValueError("The image does not record a supported Debian snapshot")
    return "\n".join(entries)


if __name__ == "__main__":
    try:
        pathlib.Path(sys.argv[2]).write_text(snapshot_sources(pathlib.Path(sys.argv[1]).read_text()), newline="\n")
    except (ValueError, IndexError) as error:
        raise SystemExit(str(error)) from error
