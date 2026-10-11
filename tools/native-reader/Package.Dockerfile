FROM scratch
ARG TARGETARCH
COPY ${TARGETARCH}/brine-native-reader /brine-native-reader
COPY ${TARGETARCH}/pins.json ${TARGETARCH}/upstream-LICENSE.txt ${TARGETARCH}/SHA256SUMS /usr/share/brine-native-reader/
ENTRYPOINT ["/brine-native-reader"]
