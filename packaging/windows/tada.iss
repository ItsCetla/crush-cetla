; Inno Setup script for the Windows installer of this fork.
;
; Per-user install: no admin prompt. Puts tada.exe in
; %LOCALAPPDATA%\Programs\Ta-Da, adds that folder to the user PATH, adds a
; Start menu entry and registers an uninstaller under Apps > Installed apps.
;
; Build (from the repo root, after unzipping a release archive):
;   iscc /DAppVersion=0.1.0 /DArch=x64 /DSourceDir=C:\path\to\tada_0.1.0_Windows_x86_64 ^
;        /ODist packaging\windows\tada.iss
;
; Silent install / uninstall:
;   tada_<ver>_Windows_x86_64_Setup.exe /VERYSILENT /SUPPRESSMSGBOXES
;   "%LOCALAPPDATA%\Programs\Ta-Da\unins000.exe" /VERYSILENT

#ifndef AppVersion
  #error Pass /DAppVersion=<version>
#endif
#ifndef SourceDir
  #error Pass /DSourceDir=<folder with tada.exe>
#endif
#ifndef Arch
  #define Arch "x64"
#endif
#if Arch == "arm64"
  #define ArchLabel "arm64"
  #define ArchAllowed "arm64"
#else
  #define ArchLabel "x86_64"
  #define ArchAllowed "x64compatible"
#endif

[Setup]
; Keep this AppId forever: upgrades and the uninstaller find the install by it.
AppId={{5C7E2B4A-8D1F-4E36-9A2C-C3E7A1F0B9D4}
AppName=Ta-Da
AppVersion={#AppVersion}
AppVerName=Ta-Da {#AppVersion}
AppPublisher=ItsCetla
AppPublisherURL=https://tada.cetla.dev/
AppSupportURL=https://github.com/ItsCetla/tada/issues
AppUpdatesURL=https://github.com/ItsCetla/tada/releases
DefaultDirName={localappdata}\Programs\Ta-Da
DefaultGroupName=Ta-Da
DisableProgramGroupPage=yes
DisableDirPage=auto
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed={#ArchAllowed}
ArchitecturesInstallIn64BitMode={#ArchAllowed}
ChangesEnvironment=yes
LicenseFile={#SourceDir}\LICENSE.md
SetupIconFile=tada.ico
UninstallDisplayIcon={app}\tada.exe
UninstallDisplayName=Ta-Da
OutputBaseFilename=tada_{#AppVersion}_Windows_{#ArchLabel}_Setup
#ifdef Sign
; windows-installer.yml passes /DSign and /Stada=<signtool command> when a
; code-signing certificate is set up. Inno Setup then signs Setup.exe and
; the uninstaller inside it.
SignTool=tada
SignedUninstaller=yes
#endif
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
VersionInfoVersion=0.0.0.0
VersionInfoProductTextVersion={#AppVersion}
VersionInfoDescription=Ta-Da installer

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "addtopath"; Description: "Add tada to PATH (run ""tada"" from any terminal)"; GroupDescription: "Command line:"
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts:"; Flags: unchecked

[Files]
Source: "{#SourceDir}\tada.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion isreadme
Source: "{#SourceDir}\LICENSE.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\completions\*"; DestDir: "{app}\completions"; Flags: ignoreversion recursesubdirs skipifsourcedoesntexist

[InstallDelete]
; Upgrades from v0.1.0 (named Crush) land in the old folder; drop its binary
; and shortcuts.
Type: files; Name: "{app}\crush.exe"
Type: files; Name: "{autoprograms}\Crush.lnk"
Type: files; Name: "{autodesktop}\Crush.lnk"

[Icons]
; Opens Ta-Da in a terminal in your home folder. Use "cd <project>; tada"
; from a terminal to work on a specific project.
Name: "{autoprograms}\Ta-Da"; Filename: "{app}\tada.exe"; WorkingDir: "{%USERPROFILE}"; Comment: "Ta-Da coding agent"
Name: "{autodesktop}\Ta-Da"; Filename: "{app}\tada.exe"; WorkingDir: "{%USERPROFILE}"; Tasks: desktopicon

[Run]
Filename: "https://tada.cetla.dev/"; Description: "Open the Ta-Da website"; Flags: postinstall shellexec skipifsilent unchecked

[Code]
const
  EnvKey = 'Environment';

function PathRoot: Integer;
begin
  if IsAdminInstallMode then
    Result := HKEY_LOCAL_MACHINE
  else
    Result := HKEY_CURRENT_USER;
end;

function PathKey: String;
begin
  if IsAdminInstallMode then
    Result := 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment'
  else
    Result := EnvKey;
end;

{ True when Dir is already one of the ;-separated entries in Path. }
function PathHas(Path, Dir: String): Boolean;
begin
  Result := Pos(';' + Lowercase(Dir) + ';', ';' + Lowercase(Path) + ';') > 0;
end;

procedure AddToPath(Dir: String);
var
  Path: String;
begin
  if not RegQueryStringValue(PathRoot, PathKey, 'Path', Path) then
    Path := '';
  if PathHas(Path, Dir) then
    exit;
  if (Path <> '') and (Copy(Path, Length(Path), 1) <> ';') then
    Path := Path + ';';
  RegWriteExpandStringValue(PathRoot, PathKey, 'Path', Path + Dir);
end;

procedure RemoveFromPath(Dir: String);
var
  Path, Rest, Entry, Kept: String;
  P: Integer;
  First: Boolean;
begin
  if not RegQueryStringValue(PathRoot, PathKey, 'Path', Path) then
    exit;
  if not PathHas(Path, Dir) then
    exit;
  Rest := Path + ';';
  Kept := '';
  First := True;
  { Drop only our folder; every other entry, even an empty one, stays as is. }
  while Rest <> '' do
  begin
    P := Pos(';', Rest);
    Entry := Copy(Rest, 1, P - 1);
    Delete(Rest, 1, P);
    if CompareText(Entry, Dir) <> 0 then
    begin
      if not First then
        Kept := Kept + ';';
      Kept := Kept + Entry;
      First := False;
    end;
  end;
  RegWriteExpandStringValue(PathRoot, PathKey, 'Path', Kept);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and WizardIsTaskSelected('addtopath') then
    AddToPath(ExpandConstant('{app}'));
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usPostUninstall then
    RemoveFromPath(ExpandConstant('{app}'));
end;
