# Changelog

What's new in each version of Stash, newest first. After an update, Stash shows this to its owner
in a What's new window.

<!--
Stash reads this file, so keep to its format:

- "## 1.3.0" starts a version. Once it's released, add the date: "## 1.3.0 - 2026-11-20". The
  newest version in this file is the version Stash says it is, and a release tag has to match it.
- "### Title" starts a change in that version. Under it go paragraphs and lists, in plain words
  for the people who use Stash. **Bold** and `code` work; other Markdown shows as it's written.
- A comment with "icon: name" under a title picks the change's icon, from the icons in
  ui/js/ui.js.
-->

## 1.3.1

### Stash's own icon
<!-- icon: sparkles -->
The top-left corner of every page shows Stash's icon, a chest full of files, instead of the
briefcase. So do the pages that people open from your links.

## 1.3.0 - 2026-11-16

### Links show their name in chat apps
<!-- icon: link -->
When you paste a link into WhatsApp, Slack, Teams or another chat app, the preview now shows the
link's name. It shows nothing else: not your message, and not the files. Looking at the preview
never counts as a download.

### Check received files for viruses
<!-- icon: shield -->
Stash can have every file that comes in through an upload link checked for viruses by ClamAV, a
free virus scanner that runs next to it. A file with a virus is kept and marked, or refused, as you
choose. A marked file never opens in the browser, and share links leave it out. The installation
guide shows how to set it up.

### See what's new after an update
<!-- icon: sparkles -->
After an update, Stash opens a window with what the new version brings. **Remind me later** shows
it again next time; **Got it** puts it away until the next update. To read it again, click the
version number at the bottom of the menu.

### Tick files with Ctrl+click and Shift+click
<!-- icon: check -->
Ctrl+click a file, or Cmd+click on a Mac, to tick it, as in a file manager. Shift+click another
file to tick every file in between.

### See which version is running
<!-- icon: terminal -->
The menu shows which version is running, and so do Stash's log and `stash version`.

### Fixes
<!-- icon: wrench -->
- **Select all** and Ctrl+A also work in My files itself, not only in the folders inside it.
- On a share page, a file's size stays on one line next to a long name.

## 1.2.0 - 2026-11-14

### Upload whole folders
<!-- icon: folder -->
Drag a folder onto a folder's page, or click **Upload folder**. The folders inside it come along.
This works on upload links too, and the link's limits count every file in the folder.

### Share a whole folder
<!-- icon: link -->
The link follows the folder, so files you add later are shared too, and the zip keeps the
subfolders.

### Select all files in a folder
<!-- icon: check -->
Tick every file in a folder at once with Ctrl+A or Cmd+A, the checkbox at the top of the list, or
**Select all**.

### QR codes for links
<!-- icon: qr -->
Show a link as a QR code, to open it on a phone or print it.

### Copy a link with one click
<!-- icon: copy -->
Click a link to copy it. The copy icon sits inside the link box.

### Fixes
<!-- icon: wrench -->
- Copy buttons work when Stash is opened over plain HTTP, such as `http://192.168.1.20:8080`.
- The upload and share pages fit on a phone, also with the commands open.
- A long command no longer pushes the buttons of the "link ready" window off the edge.
- Expires and Max downloads line up in the share link form, and Filter files has a search icon.

## 1.1.0 - 2026-11-10

### A trash for deleted files
<!-- icon: trash -->
Deleting moves files and folders to the trash, where you can restore them for 30 days.

### Rename files and folders
<!-- icon: pencil -->
Fix a typo or a vague name from the file's or folder's menu.

### A menu on every file and folder
<!-- icon: more -->
Every file and folder has a **⋯** button with everything you can do with it. Right-clicking a file
or folder shows the same menu.

### Wrong passwords in the log
<!-- icon: lock -->
Every wrong password is logged, so guessing shows and fail2ban can block it.

### Security fixes
<!-- icon: lock -->
- A `Range` header could get around a share's download limit.
- A visitor could reset password guessing by switching IPv6 addresses. A whole /64 now counts as
  one visitor.
- `HEAD` requests used up a share's downloads.

## 1.0.0 - 2026-11-09

### The first release
<!-- icon: briefcase -->
- **Your files:** upload them, sort them into folders, and look at photos, PDFs, videos and text
  files in the browser.
- **Upload links:** people add files within the link's size limit, and can't see anything else. A
  link can also limit the number of files, expire and have a password. Very large files can be
  sent from the command line on Windows, macOS and Linux.
- **Share links:** people view and download the files you picked, one by one or as a zip. A link
  can expire, stop after a number of downloads and have a password.
