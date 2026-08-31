import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "../Model.js" as Model

Panel {
  id: root

  moduleName: "io.github.kvm404.phonecam"
  ipcTarget: "io.github.kvm404.phonecam"
  manageIpc: false

  property var settings: ({})
  property bool doctorOpen: false

  readonly property var svc: bar?.shell?.serviceFor("io.github.kvm404.phonecam")
  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color urgent: bar ? bar.urgent : Color.urgent
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family
  readonly property string phase: svc ? String(svc.phase || "stopped") : "stopped"
  readonly property color barIconColor: {
    if (!svc) return dim
    if (svc.doctorBlocking || phase === "error") return urgent
    if (phase === "live") return barForeground
    if (phase === "waiting") return foreground
    return dim
  }
  readonly property string heroDetail: {
    if (phase === "waiting") return "Scan to pair"
    if (svc && svc.status && svc.status.phone_name) return String(svc.status.phone_name)
    if (phase === "missing-binary") return "CLI missing"
    if (phase === "error") return "Error"
    return Model.phaseLabel(phase)
  }
  readonly property var doctorIssues: {
    var list = []
    var checks = svc && svc.doctorChecks instanceof Array ? svc.doctorChecks : []
    for (var i = 0; i < checks.length; i++) {
      var check = checks[i]
      if (check && (check.status === "FAIL" || check.status === "WARN")) list.push(check)
    }
    return list
  }
  readonly property bool showQr: phase === "waiting" && svc && svc.qrSize > 0 && !svc.pairingExpired

  function pushSettings() {
    if (svc && "settings" in svc) svc.settings = settings
  }

  function toggleRun() {
    if (!svc) return
    if (svc.canStop) svc.stop()
    else svc.start()
  }

  onSettingsChanged: pushSettings()
  onSvcChanged: pushSettings()
  Component.onCompleted: pushSettings()

  onOpenedChanged: if (opened) {
    pushSettings()
    if (svc) {
      svc.refreshDoctor()
      svc.refreshTrust()
    }
    Qt.callLater(function() { keyCatcher.forceActiveFocus() })
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  IpcHandler {
    target: root.ipcTarget
    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: "󰄀"
    foreground: root.barIconColor
    useActiveColor: false
    onPressed: function(buttonCode) {
      if (buttonCode === Qt.MiddleButton) {
        if (root.svc) root.svc.refreshDoctor()
      } else {
        root.toggle()
      }
    }
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(360))
    contentHeight: panel.fittedContentHeight(contentColumn.implicitHeight, Style.space(640))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(text) {
        if (!root.svc) return
        var key = String(text || "").toLowerCase()
        if (key === "s") root.toggleRun()
        else if (key === "r") root.svc.restart()
      }

      Flickable {
        id: panelFlick
        anchors.fill: parent
        contentWidth: width
        contentHeight: contentColumn.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: contentHeight > height
        ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

        Column {
          id: contentColumn
          width: panelFlick.width
          spacing: Style.space(12)

          PanelHero {
            width: parent.width
            title: "PhoneCam"
            meta: Model.phaseLabel(root.phase)
            detail: root.heroDetail
            foreground: root.foreground
            fontFamily: root.fontFamily
            iconOpacity: root.phase === "live" ? 1.0 : (root.phase === "waiting" ? 0.9 : 0.48)
            iconComponent: Component {
              Text {
                text: "󰄀"
                color: root.barIconColor
                font.family: root.fontFamily
                font.pixelSize: Style.font.display
              }
            }
            trailingControl: Component {
              Row {
                spacing: Style.space(4)
                PanelActionButton {
                  iconText: root.svc && root.svc.canStop ? "󰓛" : "󰐊"
                  tooltipText: root.svc && root.svc.canStop ? "Stop" : "Start"
                  foreground: root.foreground
                  fontFamily: root.fontFamily
                  enabled: !!root.svc && (root.svc.canStart || root.svc.canStop)
                  onClicked: root.toggleRun()
                }
                PanelActionButton {
                  iconText: "󰑐"
                  tooltipText: "Restart"
                  foreground: root.foreground
                  fontFamily: root.fontFamily
                  enabled: !!root.svc && root.svc.canRestart
                  onClicked: if (root.svc) root.svc.restart()
                }
              }
            }
          }

          Text {
            visible: root.svc && (root.svc.actionStatus !== "" || root.svc.lastError !== "")
            width: parent.width
            text: root.svc
              ? (root.svc.actionStatus !== "" ? root.svc.actionStatus : root.svc.lastError)
              : ""
            color: root.svc && root.svc.actionStatus !== "" ? root.dim : root.urgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.WordWrap
            textFormat: Text.PlainText
          }

          Column {
            visible: root.phase === "missing-binary"
            width: parent.width
            spacing: Style.space(6)

            Text {
              width: parent.width
              text: "PhoneCam CLI not found. Install it, then reopen this panel."
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              wrapMode: Text.WordWrap
            }

            Text {
              width: parent.width
              text: "go install github.com/kvm404/phonecam/linux-cli/cmd/phonecam@latest\nOr a binary from github.com/kvm404/phonecam/releases"
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
              wrapMode: Text.WordWrap
              textFormat: Text.PlainText
            }
          }

          Column {
            visible: root.phase === "waiting"
            width: parent.width
            spacing: Style.space(8)

            PanelSectionHeader {
              text: root.svc && root.svc.pairingExpired ? "PAIRING EXPIRED" : "SCAN TO PAIR"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }

            Text {
              visible: !!(root.svc && root.svc.pairingExpired)
              width: parent.width
              text: "QR expired. Restart to mint a new token."
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
            }

            Item {
              visible: root.showQr
              width: parent.width
              height: qrCanvas.height

              Rectangle {
                id: qrCanvas
                readonly property int moduleSize: root.svc && root.svc.qrSize > 0
                  ? Math.max(4, Math.floor(Style.space(240) / root.svc.qrSize))
                  : 0

                width: root.svc ? root.svc.qrSize * moduleSize : 0
                height: width
                color: "white"
                radius: Style.cornerRadius
                anchors.horizontalCenter: parent.horizontalCenter

                Grid {
                  anchors.fill: parent
                  columns: root.svc ? root.svc.qrSize : 1

                  Repeater {
                    model: root.svc ? root.svc.qrSize * root.svc.qrSize : 0

                    Rectangle {
                      required property int index
                      readonly property int matrixRow: Math.floor(index / Math.max(1, root.svc ? root.svc.qrSize : 1))
                      readonly property int matrixColumn: index % Math.max(1, root.svc ? root.svc.qrSize : 1)

                      width: qrCanvas.moduleSize
                      height: qrCanvas.moduleSize
                      color: root.svc && root.svc.qrRows[matrixRow] && root.svc.qrRows[matrixRow].charAt(matrixColumn) === "1"
                        ? "#111111"
                        : "transparent"
                    }
                  }
                }
              }
            }
          }

          Column {
            visible: root.phase === "live" || root.phase === "silent"
            width: parent.width
            spacing: Style.space(6)

            PanelSectionHeader {
              text: root.phase === "live" ? "LIVE" : "SILENT"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }

            Text {
              width: parent.width
              text: {
                if (!root.svc || !root.svc.status) return ""
                var st = root.svc.status
                var parts = []
                if (st.phone_name) parts.push(String(st.phone_name))
                var video = Model.videoLabel(st.video)
                if (video !== "") parts.push(video)
                if (typeof st.last_rtp_ms === "number") parts.push(st.last_rtp_ms + " ms")
                return parts.join("  ·  ")
              }
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              wrapMode: Text.WordWrap
              textFormat: Text.PlainText
            }
          }

          PanelSeparator {
            visible: root.doctorIssues.length > 0
            foreground: root.foreground
          }

          Column {
            visible: root.doctorIssues.length > 0
            width: parent.width
            spacing: Style.space(6)

            CursorSurface {
              width: parent.width
              implicitHeight: Style.space(28)
              foreground: root.foreground
              fill: Style.hoverFillFor(root.foreground, Color.accent)

              Text {
                anchors.left: parent.left
                anchors.verticalCenter: parent.verticalCenter
                anchors.leftMargin: Style.space(4)
                text: "DOCTOR  " + root.doctorIssues.length
                color: root.svc && root.svc.doctorBlocking ? root.urgent : root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
                font.bold: true
                font.letterSpacing: 0.8
              }

              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: root.doctorOpen = !root.doctorOpen
              }
            }

            Column {
              visible: root.doctorOpen
              width: parent.width
              spacing: Style.space(6)

              Repeater {
                model: root.doctorIssues

                Column {
                  required property var modelData
                  width: parent.width
                  spacing: Style.space(2)

                  Text {
                    width: parent.width
                    text: "[" + modelData.status + "] " + modelData.name
                    color: modelData.status === "FAIL" ? root.urgent : root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.bodySmall
                    wrapMode: Text.WordWrap
                    textFormat: Text.PlainText
                  }

                  Text {
                    visible: String(modelData.message || "") !== ""
                    width: parent.width
                    text: String(modelData.message || "")
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                    wrapMode: Text.WordWrap
                    textFormat: Text.PlainText
                  }

                  Text {
                    visible: String(modelData.fix || "") !== ""
                    width: parent.width
                    text: "Fix: " + String(modelData.fix || "")
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                    wrapMode: Text.WordWrap
                    textFormat: Text.PlainText
                  }
                }
              }
            }
          }

          PanelSeparator {
            visible: root.svc && root.svc.trusted instanceof Array && root.svc.trusted.length > 0
            foreground: root.foreground
          }

          Column {
            visible: root.svc && root.svc.trusted instanceof Array && root.svc.trusted.length > 0
            width: parent.width
            spacing: Style.space(6)

            PanelSectionHeader {
              text: "TRUSTED PHONES"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }

            Repeater {
              model: root.svc && root.svc.trusted instanceof Array ? root.svc.trusted : []

              CursorSurface {
                required property var modelData
                width: parent.width
                implicitHeight: Style.space(40)
                foreground: root.foreground
                fill: Style.hoverFillFor(root.foreground, Color.accent)

                RowLayout {
                  anchors.fill: parent
                  anchors.leftMargin: Style.space(8)
                  anchors.rightMargin: Style.space(4)
                  spacing: Style.space(8)

                  Text {
                    Layout.fillWidth: true
                    text: String(modelData.name || modelData.id || "phone")
                    color: root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.body
                    elide: Text.ElideRight
                    textFormat: Text.PlainText
                  }

                  PanelActionButton {
                    iconText: "󰛌"
                    tooltipText: "Revoke trust"
                    foreground: root.foreground
                    hoverColor: root.urgent
                    fontFamily: root.fontFamily
                    onClicked: if (root.svc) root.svc.revokeTrust(modelData.id)
                  }
                }
              }
            }
          }

          Text {
            width: parent.width
            text: "Stream is unencrypted local RTP."
            color: Qt.darker(root.dim, 1.18)
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            horizontalAlignment: Text.AlignHCenter
            wrapMode: Text.WordWrap
          }

          Text {
            width: parent.width
            text: "s start/stop  ·  r restart  ·  Esc close"
            color: Qt.darker(root.dim, 1.18)
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            horizontalAlignment: Text.AlignHCenter
          }
        }
      }
    }
  }
}
