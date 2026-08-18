package com.kvm404.phonecam.pairing

/** Outcome of a Home-screen Reconnect tap (new process / after Leave). */
sealed interface HomeReconnectResult {
    data class Ready(
        val payload: PairingPayload,
        val resumeToken: String,
        val pairingSecret: String,
        val profile: VideoProfile,
        val rtp: RtpIdentity,
    ) : HomeReconnectResult

    data class Failure(val message: String) : HomeReconnectResult
}

/**
 * One-shot POST /reconnect from Home using a stored pairing_secret.
 * Distinct from [ReconnectController], which is the in-session Live path.
 */
class HomeReconnect(
    private val client: ControlClient,
) {
    fun connect(
        laptop: TrustedLaptop,
        phone: PhoneIdentity,
        rtp: RtpIdentity,
        video: VideoProfile,
        camera: String? = null,
    ): HomeReconnectResult {
        val payload = payloadFor(laptop, video)
        val result = try {
            client.reconnect(
                ReconnectRequest(
                    payload = payload,
                    phone = phone,
                    rtpPort = rtp.sourcePort,
                    ssrc = rtp.ssrc,
                    video = video,
                    pairingSecret = laptop.secret,
                    camera = camera,
                )
            )
        } catch (t: Exception) {
            return HomeReconnectResult.Failure(unreachable(laptop.name))
        }
        return when (result) {
            is ReconnectResult.Ok -> {
                val control = result.control.ifBlank { laptop.control }
                val rtpAddr = result.rtp.ifBlank { laptop.rtp }
                HomeReconnectResult.Ready(
                    payload = payload.copy(
                        name = laptop.name,
                        control = control,
                        rtp = rtpAddr,
                        session = result.session.ifBlank { payload.session },
                        laptopId = laptop.laptopId,
                    ),
                    resumeToken = result.resumeToken,
                    pairingSecret = laptop.secret,
                    profile = video,
                    rtp = rtp,
                )
            }
            is ReconnectResult.Failure -> HomeReconnectResult.Failure(
                failureMessage(laptop.name, result.message)
            )
        }
    }

    companion object {
        fun unreachable(name: String): String =
            "Can't reach $name — scan the QR"

        fun inUse(): String =
            "PhoneCam is in use — stop it on the laptop or revoke the other phone."

        fun failureMessage(name: String, serverMessage: String): String {
            val lower = serverMessage.lowercase()
            if (lower.contains("different phone") || lower.contains("in use")) {
                return inUse()
            }
            return unreachable(name)
        }

        fun payloadFor(laptop: TrustedLaptop, video: VideoProfile): PairingPayload {
            val colon = laptop.rtp.lastIndexOf(':')
            val host = if (colon > 0) laptop.rtp.substring(0, colon) else laptop.rtp
            val port = if (colon > 0) {
                laptop.rtp.substring(colon + 1).toIntOrNull() ?: 47471
            } else {
                47471
            }
            return PairingPayload(
                version = PairingPayload.PROTOCOL_VERSION,
                name = laptop.name,
                control = laptop.control,
                rtp = laptop.rtp,
                rtpHost = host,
                rtpPort = port,
                session = "",
                token = "trusted",
                expires = "2999-01-01T00:00:00Z",
                transport = PairingPayload.TRANSPORT,
                video = video,
                laptopId = laptop.laptopId,
            )
        }
    }
}
