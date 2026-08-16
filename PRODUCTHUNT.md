# Product Hunt launch copy

Paste these into the launch form. The **Description** field is the one Product Hunt asks as: *What’s new or different about your launch compared to existing products? Which features make it stand out?*

Official docs disagree on 260 vs 500 characters. Use the 500-character version on the form; the first ~250 characters are what the feed shows.

## Name

Sprout

## Tagline (60 characters max)

Instant Postgres branches from the DB you already run

(53 characters)

Alternates:

- Branch real Postgres without moving off Supabase (48)
- CoW Postgres branches. Keep the database you have. (50)

## Description (paste this)

Neon and hosted platforms only branch databases they own. Sprout branches the Postgres you already run — including Supabase.

Connect once. Sprout keeps a live local replica, then ZFS/APFS copy-on-write spins a writable branch in about a second: full data, its own hostname, no extra prod replication slot.

Open source and self-hosted. GitHub login isolates each teammate's connectors and branches on one VM. Reset or delete a branch when you're done.

(452 characters)

Shorter backup if the form caps at 260:

Sprout branches the Postgres you already run — Supabase included. No migration. One connect keeps a live replica; ZFS/APFS copy-on-write then gives each teammate a writable branch in ~1s, with their own URL. Open source, self-hosted, GitHub-isolated.

(250 characters)

## Suggested tags

developer-tools, postgres, open-source

## First maker comment (optional)

Hey Product Hunt — I’m Aditya.

We kept hitting the same wall: preview apps and feature work need a real Postgres, but the options were a shared staging DB, a slow dump/restore, or moving the whole database onto a host that sells branching.

Sprout is the other path. You point it at the Postgres you already have (Supabase works via logical replication). It keeps a local replica, then copy-on-write (ZFS on Linux, APFS on a Mac) clones that data directory into an independent, writable Postgres in about a second. Each branch gets its own URL. The replica keeps syncing; the branch does not steal the production replication slot.

It’s open source and self-hosted. One VM, GitHub login, and every teammate only sees their own connectors and branches.

Repo: https://github.com/adityaraj-09/sprout

If you try it, `sprout connect --mode=logical` then `sprout branch create` is the whole loop. I’d love feedback from anyone living on Supabase or a single shared staging database.
