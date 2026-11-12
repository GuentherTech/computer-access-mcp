#define AppName "Computer Access"
#define AppExe "computer-access-mcp.exe"
#define AppVersion GetStringFileInfo(SourcePath + "..\dist\" + AppExe, "ProductVersion")
#ifndef CloudflaredPath
  #define CloudflaredPath SourcePath + "..\dist\cloudflared.exe"
#endif

[Setup]
AppId=GuentherTech.ComputerAccess
AppName={#AppName}
AppVerName={#AppName}
AppVersion={#AppVersion}
UninstallDisplayName={#AppName}
VersionInfoDescription={#AppName} Setup
AppPublisher=GuentherTech
DefaultDirName={localappdata}\Programs\ComputerAccess
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
DisableProgramGroupPage=yes
DisableWelcomePage=yes
WizardStyle=modern
OutputDir=..\dist
OutputBaseFilename=computer-access-setup
SetupIconFile=..\assets\app.ico
UninstallDisplayIcon={app}\{#AppExe}
AppMutex=Local\GuentherTech.ComputerAccess
CloseApplications=yes
RestartApplications=no
Compression=lzma2
SolidCompression=yes

[Tasks]
Name: "startup"; Description: "Start at login"; Flags: unchecked

[Files]
Source: "..\dist\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#CloudflaredPath}"; DestDir: "{app}"; DestName: "cloudflared.exe"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"; AppUserModelID: "GuentherTech.ComputerAccess"
Name: "{userstartup}\{#AppName}"; Filename: "{app}\{#AppExe}"; Parameters: "agent -open=false"; Tasks: startup; AppUserModelID: "GuentherTech.ComputerAccess"

[Run]
Filename: "{app}\{#AppExe}"; Description: "Open {#AppName}"; Flags: nowait postinstall skipifsilent
