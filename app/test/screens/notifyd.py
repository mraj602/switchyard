#!/usr/bin/env python3
"""A fake org.freedesktop.Notifications server for test/screens/screens.mjs.

Run it on a private session bus only (dbus-run-session): it owns the notification name there
and writes one JSON line per Notify call (app name, icon, summary, body, hint names and the
desktop-entry / image-path hints) to the file given, so the screenshot audit can check what
the app sends to the desktop without a real notification daemon.
"""
import json
import sys

import dbus
import dbus.service
from dbus.mainloop.glib import DBusGMainLoop
from gi.repository import GLib

OUT = sys.argv[1]


class Notifications(dbus.service.Object):
    def __init__(self, bus):
        super().__init__(bus, "/org/freedesktop/Notifications")
        self.n = 0

    @dbus.service.method("org.freedesktop.Notifications", in_signature="", out_signature="as")
    def GetCapabilities(self):
        return ["body", "actions", "icon-static", "persistence"]

    @dbus.service.method("org.freedesktop.Notifications", in_signature="", out_signature="ssss")
    def GetServerInformation(self):
        return ("sy-fake-notifyd", "switchyard", "1", "1.2")

    @dbus.service.method("org.freedesktop.Notifications", in_signature="susssasa{sv}i", out_signature="u")
    def Notify(self, app_name, replaces_id, app_icon, summary, body, actions, hints, timeout):
        self.n += 1
        rec = {
            "app_name": str(app_name),
            "app_icon": str(app_icon),
            "summary": str(summary),
            "body": str(body),
            "actions": [str(a) for a in actions],
            "hints": sorted(str(k) for k in hints.keys()),
            "desktop_entry": str(hints.get("desktop-entry", "")),
            "image_path": str(hints.get("image-path", hints.get("image_path", ""))),
            "has_image_data": any(k in hints for k in ("image-data", "image_data", "icon_data")),
        }
        with open(OUT, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec) + "\n")
        return self.n

    @dbus.service.method("org.freedesktop.Notifications", in_signature="u", out_signature="")
    def CloseNotification(self, _id):
        return None


def main():
    DBusGMainLoop(set_as_default=True)
    bus = dbus.SessionBus()
    name = dbus.service.BusName("org.freedesktop.Notifications", bus, do_not_queue=True)
    _keep = (name, Notifications(bus))
    GLib.MainLoop().run()


if __name__ == "__main__":
    main()
