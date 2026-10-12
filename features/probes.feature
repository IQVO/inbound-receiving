# Liveness probe.
#
# Derived from: apis/openapi.yaml GET /healthz - 200 while the process is
# running, with a status document.

Feature: Health probe
  As the platform
  I want a liveness endpoint
  So that a running process is never restarted

  @bdd
  Scenario: Liveness answers 200 with a status
    When I GET "/healthz"
    Then the response status is 200
    And the response field "status" is "ok"
