' ============================================================
'  GitHub Dashboard - hidden console launcher
'  Double-click: starts the server with NO console window,
'  waits until it responds, then opens your browser.
'  Shows a message box if it fails to start.
' ============================================================
Option Explicit
Dim fso, shell, basePath, cmd, ports, i, port, ready

Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
basePath = fso.GetParentFolderName(WScript.ScriptFullName)
shell.CurrentDirectory = basePath

cmd = "cmd /c """ & basePath & "\start.bat" & """"
shell.Run cmd, 0, False

ports = Array(8000, 8080, 8888, 5000, 3000, 9000)
ready = False
For i = 1 To 40
    WScript.Sleep 500
    For Each port In ports
        If HttpOk(CStr(port)) Then ready = True : Exit For
    Next
    If ready Then Exit For
Next

If Not ready Then
    MsgBox "Dashboard failed to start (no response on any candidate port)." & vbCrLf & _
           "Please run start.bat to see the error.", 16, "GitHub Dashboard"
End If

Function HttpOk(p)
    On Error Resume Next
    Dim http
    Set http = CreateObject("MSXML2.XMLHTTP")
    http.open "GET", "http://127.0.0.1:" & p & "/", False
    http.send
    HttpOk = False
    If Err.Number = 0 And http.Status = 200 Then HttpOk = True
    On Error GoTo 0
End Function
