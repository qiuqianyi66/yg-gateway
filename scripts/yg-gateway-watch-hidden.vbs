Set ws = CreateObject("WScript.Shell")
ws.Run "powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File ""%USERPROFILE%\Desktop\yg-gateway\scripts\yg-gateway-watch.ps1""", 0, False
